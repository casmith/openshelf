package discover

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/images"
	"github.com/casmith/openshelf/internal/providers"
)

var (
	ErrNoSource      = errors.New("discover: item was not imported from a provider")
	ErrNoProvider    = errors.New("discover: provider is not available")
	ErrInvalidImport = errors.New("discover: provider item is incomplete")
)

// Lockable media_items fields; names match the columns and are what locked_fields stores.
const (
	FieldTitle         = "title"
	FieldOriginalTitle = "original_title"
	FieldReleaseDate   = "release_date"
	FieldReleaseYear   = "release_year"
	FieldSynopsis      = "synopsis"
	FieldGenres        = "genres"
	FieldCover         = "cover_image_id"
	FieldCredits       = "credits"
)

// Import upserts a provider item into the catalog in one transaction and returns it.
// It is idempotent by the item's external IDs; an existing item is refreshed, skipping locked fields.
func Import(ctx context.Context, d *deps.Deps, providerName string, it *providers.Item, userID *int64) (db.MediaItem, error) {
	if it == nil || it.Title == "" || !it.Kind.Valid() || len(it.ExternalIDs) == 0 {
		return db.MediaItem{}, ErrInvalidImport
	}
	var out db.MediaItem
	err := d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		var err error
		out, err = importTx(ctx, d, q, tx, providerName, it, userID)
		return err
	})
	return out, err
}

func importTx(ctx context.Context, d *deps.Deps, q *db.Queries, tx pgx.Tx, providerName string, it *providers.Item, userID *int64) (db.MediaItem, error) {
	primary := it.ExternalIDs[0]
	if err := q.DiscoverLockExternalID(ctx, db.DiscoverLockExternalIDParams{Provider: primary.Provider, Value: primary.Value}); err != nil {
		return db.MediaItem{}, err
	}
	existing, found, err := findExisting(ctx, q, it.ExternalIDs)
	if err != nil {
		return db.MediaItem{}, err
	}
	now := d.Now()
	var locked []string
	if found {
		if existing.Kind != it.Kind {
			return db.MediaItem{}, fmt.Errorf("%w: external ID belongs to a %s", ErrInvalidImport, existing.Kind)
		}
		locked = existing.LockedFields
	}
	isLocked := func(f string) bool { return slices.Contains(locked, f) }

	var coverID *int64
	if found {
		coverID = existing.CoverImageID
	}
	if it.CoverURL != "" && !isLocked(FieldCover) {
		id, err := images.Enqueue(ctx, d, tx, it.CoverURL)
		if err != nil {
			return db.MediaItem{}, err
		}
		coverID = &id
	}

	title := strings.TrimSpace(it.Title)
	genres := normalizeGenres(it.Genres)
	var item db.MediaItem
	if !found {
		item, err = q.DiscoverInsertItem(ctx, db.DiscoverInsertItemParams{
			Kind: it.Kind, Title: title, SortTitle: catalog.SortTitle(title),
			OriginalTitle: optString(it.OriginalTitle), ReleaseDate: it.ReleaseDate, ReleaseYear: releaseYear(it),
			Synopsis: optString(it.Synopsis), Genres: genres, CoverImageID: coverID,
			MetadataProvider: &providerName, MetadataRefreshedAt: &now, CreatedByUserID: userID,
		})
	} else {
		p := db.DiscoverUpdateItemParams{
			ID: existing.ID, Title: title, SortTitle: catalog.SortTitle(title),
			OriginalTitle: optString(it.OriginalTitle), ReleaseDate: it.ReleaseDate, ReleaseYear: releaseYear(it),
			Synopsis: optString(it.Synopsis), Genres: genres, CoverImageID: coverID,
			MetadataProvider: &providerName, MetadataRefreshedAt: &now,
		}
		if isLocked(FieldTitle) {
			p.Title, p.SortTitle = existing.Title, existing.SortTitle
		}
		if isLocked(FieldOriginalTitle) {
			p.OriginalTitle = existing.OriginalTitle
		}
		if isLocked(FieldReleaseDate) {
			p.ReleaseDate = existing.ReleaseDate
		}
		if isLocked(FieldReleaseYear) {
			p.ReleaseYear = existing.ReleaseYear
		}
		if isLocked(FieldSynopsis) {
			p.Synopsis = existing.Synopsis
		}
		if isLocked(FieldGenres) {
			p.Genres = existing.Genres
		}
		item, err = q.DiscoverUpdateItem(ctx, p)
	}
	if err != nil {
		return db.MediaItem{}, err
	}

	if err := upsertDetails(ctx, q, item.ID, it, isLocked); err != nil {
		return db.MediaItem{}, err
	}
	if !isLocked(FieldCredits) {
		if err := replaceCredits(ctx, q, item.ID, it.Credits); err != nil {
			return db.MediaItem{}, err
		}
	}
	for _, x := range it.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		if err := q.DiscoverUpsertItemExternalID(ctx, db.DiscoverUpsertItemExternalIDParams{
			Provider: x.Provider, Value: x.Value, Url: optString(x.URL), MediaItemID: &item.ID,
		}); err != nil {
			return db.MediaItem{}, err
		}
	}
	for _, e := range it.Editions {
		if err := upsertEdition(ctx, d, q, tx, item.ID, it.Kind, e, found); err != nil {
			return db.MediaItem{}, err
		}
	}
	if err := q.DiscoverUpsertSource(ctx, db.DiscoverUpsertSourceParams{
		MediaItemID: item.ID, Provider: providerName, ExternalID: primary.Value,
	}); err != nil {
		return db.MediaItem{}, err
	}
	return item, nil
}

func findExisting(ctx context.Context, q *db.Queries, ids []providers.ExternalID) (db.MediaItem, bool, error) {
	for _, x := range ids {
		m, err := q.FindMediaItemByExternalID(ctx, db.FindMediaItemByExternalIDParams{Provider: x.Provider, Value: x.Value})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return db.MediaItem{}, false, err
		}
		m, err = q.DiscoverGetItemForUpdate(ctx, m.ID)
		return m, err == nil, err
	}
	return db.MediaItem{}, false, nil
}

func upsertDetails(ctx context.Context, q *db.Queries, itemID int64, it *providers.Item, isLocked func(string) bool) error {
	switch it.Kind {
	case db.MediaKindGame:
		p := db.DiscoverUpsertGameDetailsParams{MediaItemID: itemID}
		if it.Game != nil {
			p.Franchise = optString(it.Game.Franchise)
		}
		if cur, err := q.GetGameDetails(ctx, itemID); err == nil && isLocked("franchise") {
			p.Franchise = cur.Franchise
		}
		return q.DiscoverUpsertGameDetails(ctx, p)
	case db.MediaKindBook:
		p := db.DiscoverUpsertBookDetailsParams{MediaItemID: itemID}
		if it.Book != nil {
			p.SeriesName, p.SeriesPosition = optString(it.Book.SeriesName), it.Book.SeriesPosition
		}
		if cur, err := q.GetBookDetails(ctx, itemID); err == nil {
			if isLocked("series_name") {
				p.SeriesName = cur.SeriesName
			}
			if isLocked("series_position") {
				p.SeriesPosition = cur.SeriesPosition
			}
		}
		return q.DiscoverUpsertBookDetails(ctx, p)
	case db.MediaKindFilm:
		p := db.DiscoverUpsertFilmDetailsParams{MediaItemID: itemID}
		if it.Film != nil {
			p.RuntimeMinutes = it.Film.RuntimeMinutes
		}
		if cur, err := q.GetFilmDetails(ctx, itemID); err == nil && isLocked("runtime_minutes") {
			p.RuntimeMinutes = cur.RuntimeMinutes
		}
		return q.DiscoverUpsertFilmDetails(ctx, p)
	case db.MediaKindShow:
		p := db.DiscoverUpsertShowDetailsParams{MediaItemID: itemID}
		if it.Show != nil {
			p.Network, p.Status = optString(it.Show.Network), optString(it.Show.Status)
			p.SeasonCount, p.EpisodeCount = it.Show.SeasonCount, it.Show.EpisodeCount
		}
		if cur, err := q.GetShowDetails(ctx, itemID); err == nil {
			if isLocked("network") {
				p.Network = cur.Network
			}
			if isLocked("status") {
				p.Status = cur.Status
			}
			if isLocked("season_count") {
				p.SeasonCount = cur.SeasonCount
			}
			if isLocked("episode_count") {
				p.EpisodeCount = cur.EpisodeCount
			}
		}
		return q.DiscoverUpsertShowDetails(ctx, p)
	}
	return nil
}

func replaceCredits(ctx context.Context, q *db.Queries, itemID int64, credits []providers.Credit) error {
	if err := q.DiscoverDeleteCredits(ctx, itemID); err != nil {
		return err
	}
	pos := map[db.CreditRole]int16{}
	for _, c := range credits {
		name := strings.TrimSpace(c.Name)
		if name == "" || !c.Role.Valid() {
			continue
		}
		creatorID, err := q.DiscoverFindCreatorByName(ctx, name)
		if errors.Is(err, pgx.ErrNoRows) {
			creatorID, err = q.DiscoverInsertCreator(ctx, db.DiscoverInsertCreatorParams{Name: name, SortName: name})
		}
		if err != nil {
			return err
		}
		if err := q.DiscoverInsertCredit(ctx, db.DiscoverInsertCreditParams{
			MediaItemID: itemID, CreatorID: creatorID, Role: c.Role, Position: pos[c.Role],
		}); err != nil {
			return err
		}
		pos[c.Role]++
	}
	return nil
}

// upsertEdition matches editions by their external IDs. Editions without IDs can't be
// matched, so they're only added when the item is first imported.
func upsertEdition(ctx context.Context, d *deps.Deps, q *db.Queries, tx pgx.Tx, itemID int64, kind db.MediaKind, e providers.Edition, refresh bool) error {
	var editionID int64
	for _, x := range e.ExternalIDs {
		id, err := q.DiscoverFindEditionByExternalID(ctx, db.DiscoverFindEditionByExternalIDParams{MediaItemID: itemID, Provider: x.Provider, Value: x.Value})
		if err == nil {
			editionID = id
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if editionID == 0 && refresh && len(e.ExternalIDs) == 0 {
		return nil
	}

	var platformID *int64
	if kind == db.MediaKindGame && e.Platform != nil {
		var err error
		if platformID, err = resolvePlatform(ctx, d, q, *e.Platform); err != nil {
			return err
		}
	}
	var coverID *int64
	if e.CoverURL != "" {
		id, err := images.Enqueue(ctx, d, tx, e.CoverURL)
		if err != nil {
			return err
		}
		coverID = &id
	}
	var format *db.EditionFormat
	if e.Format != "" && e.Format.Valid() {
		format = &e.Format
	}
	if editionID == 0 {
		id, err := q.DiscoverInsertEdition(ctx, db.DiscoverInsertEditionParams{
			MediaItemID: itemID, Title: optString(e.Title), Format: format, PlatformID: platformID,
			Region: optString(e.Region), Publisher: optString(e.Publisher), ReleaseDate: e.ReleaseDate,
			PageCount: e.PageCount, DurationMinutes: e.DurationMinutes, CoverImageID: coverID,
		})
		if err != nil {
			return err
		}
		editionID = id
	} else if err := q.DiscoverUpdateEdition(ctx, db.DiscoverUpdateEditionParams{
		ID: editionID, Title: optString(e.Title), Format: format, PlatformID: platformID,
		Region: optString(e.Region), Publisher: optString(e.Publisher), ReleaseDate: e.ReleaseDate,
		PageCount: e.PageCount, DurationMinutes: e.DurationMinutes, CoverImageID: coverID,
	}); err != nil {
		return err
	}
	for _, x := range e.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		if err := q.DiscoverUpsertEditionExternalID(ctx, db.DiscoverUpsertEditionExternalIDParams{
			Provider: x.Provider, Value: x.Value, Url: optString(x.URL), EditionID: &editionID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// resolvePlatform maps a provider platform to a local one; it never creates platforms.
func resolvePlatform(ctx context.Context, d *deps.Deps, q *db.Queries, ref providers.PlatformRef) (*int64, error) {
	if x := ref.ExternalID; x != nil && x.Provider != "" && x.Value != "" {
		id, err := q.DiscoverFindPlatformByExternalID(ctx, db.DiscoverFindPlatformByExternalIDParams{Provider: x.Provider, Value: x.Value})
		if err == nil {
			return &id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if name := strings.TrimSpace(ref.Name); name != "" {
		p, err := q.FindPlatformByName(ctx, name)
		if err == nil {
			if x := ref.ExternalID; x != nil && x.Provider != "" && x.Value != "" {
				if err := q.DiscoverInsertPlatformExternalID(ctx, db.DiscoverInsertPlatformExternalIDParams{
					Provider: x.Provider, Value: x.Value, Url: optString(x.URL), PlatformID: &p.ID,
				}); err != nil {
					return nil, err
				}
			}
			return &p.ID, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	args := []any{"name", ref.Name}
	if ref.ExternalID != nil {
		args = append(args, "provider", ref.ExternalID.Provider, "value", ref.ExternalID.Value)
	}
	d.Log.Warn("discover: unresolved platform", args...)
	return nil, nil
}

// Refresh re-fetches an imported item from its provider and updates it.
func Refresh(ctx context.Context, d *deps.Deps, itemID int64) (db.MediaItem, error) {
	src, err := d.Q.DiscoverGetSource(ctx, itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.MediaItem{}, ErrNoSource
	}
	if err != nil {
		return db.MediaItem{}, err
	}
	item, err := d.Q.GetMediaItemByID(ctx, itemID)
	if err != nil {
		return db.MediaItem{}, err
	}
	p, ok := d.Providers.Get(src.Provider)
	if !ok || !p.Configured() {
		return db.MediaItem{}, fmt.Errorf("%w: %s", ErrNoProvider, src.Provider)
	}
	fctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	it, err := p.Fetch(fctx, item.Kind, src.ExternalID)
	recordResult(p.Name(), err, d.Now())
	if err != nil {
		return db.MediaItem{}, err
	}
	return Import(ctx, d, p.Name(), it, nil)
}

// RefreshStale refreshes up to limit imported items not refreshed since cutoff.
// Failures are logged and skipped; it returns how many items were refreshed.
func RefreshStale(ctx context.Context, d *deps.Deps, cutoff time.Time, limit int) (int, error) {
	ids, err := d.Q.DiscoverListStale(ctx, db.DiscoverListStaleParams{Cutoff: cutoff, Lim: int32(limit)})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if _, err := Refresh(ctx, d, id); err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			d.Log.Warn("discover: refresh failed", "media_item_id", id, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

func releaseYear(it *providers.Item) *int16 {
	y := it.ReleaseYear
	if y == 0 && it.ReleaseDate != nil {
		y = it.ReleaseDate.Year()
	}
	if y <= 0 || y > 32767 {
		return nil
	}
	v := int16(y)
	return &v
}

func normalizeGenres(in []string) []string {
	out := []string{}
	for _, g := range in {
		g = strings.ToLower(strings.TrimSpace(g))
		if g != "" && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

func optString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
