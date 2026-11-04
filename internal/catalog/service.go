package catalog

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/db"
)

type CreditInput struct {
	Name string
	Role db.CreditRole
}

// ItemInput holds the editable fields of an item plus its kind's details row and credits.
type ItemInput struct {
	Kind           db.MediaKind
	Title          string
	OriginalTitle  *string
	ReleaseDate    *time.Time
	ReleaseYear    *int16
	Synopsis       *string
	Genres         []string
	Franchise      *string
	SeriesName     *string
	SeriesPosition *float64
	RuntimeMinutes *int32
	Network        *string
	Status         *string
	SeasonCount    *int32
	EpisodeCount   *int32
	Credits        []CreditInput
}

type EditionInput struct {
	Title           *string
	Format          *db.EditionFormat
	PlatformID      *int64
	Region          *string
	Publisher       *string
	ReleaseDate     *time.Time
	PageCount       *int32
	DurationMinutes *int32
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func num[T int16 | int32 | int64](p *T) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(int64(*p), 10)
}

func dateStr(p *time.Time) string {
	if p == nil {
		return ""
	}
	return p.Format(time.DateOnly)
}

func floatStr(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// itemFields maps each form field (and locked_fields name) to its canonical string value.
var itemFields = []struct {
	name string
	val  func(ItemInput) string
}{
	{"title", func(i ItemInput) string { return i.Title }},
	{"original_title", func(i ItemInput) string { return str(i.OriginalTitle) }},
	{"release_date", func(i ItemInput) string { return dateStr(i.ReleaseDate) }},
	{"release_year", func(i ItemInput) string { return num(i.ReleaseYear) }},
	{"synopsis", func(i ItemInput) string { return str(i.Synopsis) }},
	{"genres", func(i ItemInput) string { return strings.Join(i.Genres, ", ") }},
	{"franchise", func(i ItemInput) string { return str(i.Franchise) }},
	{"series_name", func(i ItemInput) string { return str(i.SeriesName) }},
	{"series_position", func(i ItemInput) string { return floatStr(i.SeriesPosition) }},
	{"runtime_minutes", func(i ItemInput) string { return num(i.RuntimeMinutes) }},
	{"network", func(i ItemInput) string { return str(i.Network) }},
	{"status", func(i ItemInput) string { return str(i.Status) }},
	{"season_count", func(i ItemInput) string { return num(i.SeasonCount) }},
	{"episode_count", func(i ItemInput) string { return num(i.EpisodeCount) }},
}

// Values renders the input as form values for pre-filling the edit form.
func (in ItemInput) Values() url.Values {
	v := url.Values{"kind": {string(in.Kind)}}
	for _, f := range itemFields {
		v.Set(f.name, f.val(in))
	}
	for _, c := range in.Credits {
		v.Add("credit_name", c.Name)
		v.Add("credit_role", string(c.Role))
	}
	return v
}

func creditsKey(cs []CreditInput) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(string(c.Role) + ":" + strings.ToLower(c.Name) + "\n")
	}
	return b.String()
}

// ChangedFields lists the fields (as locked_fields names) that differ between two inputs.
func ChangedFields(old, updated ItemInput) []string {
	var out []string
	for _, f := range itemFields {
		if f.val(old) != f.val(updated) {
			out = append(out, f.name)
		}
	}
	if creditsKey(old.Credits) != creditsKey(updated.Credits) {
		out = append(out, "credits")
	}
	return out
}

func noRows[T any](v T, err error) (T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		var zero T
		return zero, nil
	}
	return v, err
}

// LoadItemInput reads an item's current editable state, including details and credits.
func LoadItemInput(ctx context.Context, q *db.Queries, item db.MediaItem) (ItemInput, error) {
	in := ItemInput{
		Kind: item.Kind, Title: item.Title, OriginalTitle: item.OriginalTitle,
		ReleaseDate: item.ReleaseDate, ReleaseYear: item.ReleaseYear, Synopsis: item.Synopsis, Genres: item.Genres,
	}
	switch item.Kind {
	case db.MediaKindGame:
		g, err := noRows(q.GetGameDetails(ctx, item.ID))
		if err != nil {
			return in, err
		}
		in.Franchise = g.Franchise
	case db.MediaKindBook:
		b, err := noRows(q.GetBookDetails(ctx, item.ID))
		if err != nil {
			return in, err
		}
		in.SeriesName, in.SeriesPosition = b.SeriesName, b.SeriesPosition
	case db.MediaKindFilm:
		f, err := noRows(q.GetFilmDetails(ctx, item.ID))
		if err != nil {
			return in, err
		}
		in.RuntimeMinutes = f.RuntimeMinutes
	case db.MediaKindShow:
		s, err := noRows(q.GetShowDetails(ctx, item.ID))
		if err != nil {
			return in, err
		}
		in.Network, in.Status, in.SeasonCount, in.EpisodeCount = s.Network, s.Status, s.SeasonCount, s.EpisodeCount
	}
	credits, err := q.ListCreditsForItems(ctx, []int64{item.ID})
	if err != nil {
		return in, err
	}
	for _, c := range credits {
		in.Credits = append(in.Credits, CreditInput{Name: c.Name, Role: c.Role})
	}
	return in, nil
}

func CreateItem(ctx context.Context, q *db.Queries, creatorID int64, in ItemInput) (db.MediaItem, error) {
	item, err := q.CatalogCreateItem(ctx, db.CatalogCreateItemParams{
		Kind: in.Kind, Title: in.Title, SortTitle: SortTitle(in.Title), OriginalTitle: in.OriginalTitle,
		ReleaseDate: in.ReleaseDate, ReleaseYear: in.ReleaseYear, Synopsis: in.Synopsis,
		Genres: nonNil(in.Genres), CreatedByUserID: &creatorID,
	})
	if err != nil {
		return item, err
	}
	if err := saveDetails(ctx, q, item.ID, in); err != nil {
		return item, err
	}
	return item, saveCredits(ctx, q, item.ID, in.Credits)
}

// UpdateItem saves in over item. Changed fields of a provider-sourced item are added
// to locked_fields so a provider refresh won't overwrite the manual correction.
func UpdateItem(ctx context.Context, q *db.Queries, item db.MediaItem, in ItemInput) (db.MediaItem, error) {
	in.Kind = item.Kind
	old, err := LoadItemInput(ctx, q, item)
	if err != nil {
		return item, err
	}
	locked := slices.Clone(item.LockedFields)
	if item.MetadataProvider != nil {
		for _, f := range ChangedFields(old, in) {
			if !slices.Contains(locked, f) {
				locked = append(locked, f)
			}
		}
	}
	updated, err := q.CatalogUpdateItem(ctx, db.CatalogUpdateItemParams{
		ID: item.ID, Title: in.Title, SortTitle: SortTitle(in.Title), OriginalTitle: in.OriginalTitle,
		ReleaseDate: in.ReleaseDate, ReleaseYear: in.ReleaseYear, Synopsis: in.Synopsis,
		Genres: nonNil(in.Genres), LockedFields: nonNil(locked),
	})
	if err != nil {
		return item, err
	}
	if err := saveDetails(ctx, q, item.ID, in); err != nil {
		return updated, err
	}
	if creditsKey(old.Credits) == creditsKey(in.Credits) {
		return updated, nil
	}
	if err := q.CatalogDeleteCredits(ctx, item.ID); err != nil {
		return updated, err
	}
	return updated, saveCredits(ctx, q, item.ID, in.Credits)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func saveDetails(ctx context.Context, q *db.Queries, id int64, in ItemInput) error {
	switch in.Kind {
	case db.MediaKindGame:
		return q.CatalogUpsertGameDetails(ctx, db.CatalogUpsertGameDetailsParams{MediaItemID: id, Franchise: in.Franchise})
	case db.MediaKindBook:
		return q.CatalogUpsertBookDetails(ctx, db.CatalogUpsertBookDetailsParams{MediaItemID: id, SeriesName: in.SeriesName, SeriesPosition: in.SeriesPosition})
	case db.MediaKindFilm:
		return q.CatalogUpsertFilmDetails(ctx, db.CatalogUpsertFilmDetailsParams{MediaItemID: id, RuntimeMinutes: in.RuntimeMinutes})
	case db.MediaKindShow:
		return q.CatalogUpsertShowDetails(ctx, db.CatalogUpsertShowDetailsParams{
			MediaItemID: id, Network: in.Network, Status: in.Status, SeasonCount: in.SeasonCount, EpisodeCount: in.EpisodeCount,
		})
	}
	return nil
}

func saveCredits(ctx context.Context, q *db.Queries, itemID int64, credits []CreditInput) error {
	positions := map[db.CreditRole]int16{}
	for _, c := range credits {
		creator, err := FindOrCreateCreator(ctx, q, c.Name, c.Role)
		if err != nil {
			return err
		}
		if err := q.CatalogInsertCredit(ctx, db.CatalogInsertCreditParams{
			MediaItemID: itemID, CreatorID: creator.ID, Role: c.Role, Position: positions[c.Role],
		}); err != nil {
			return err
		}
		positions[c.Role]++
	}
	return nil
}

// FindOrCreateCreator matches an existing creator by case-insensitive name.
func FindOrCreateCreator(ctx context.Context, q *db.Queries, name string, role db.CreditRole) (db.Creator, error) {
	c, err := q.CatalogFindCreatorByName(ctx, name)
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	return q.CatalogCreateCreator(ctx, db.CatalogCreateCreatorParams{Name: name, SortName: CreatorSortName(name, role)})
}

// CreatorSortName inverts personal names ("Ursula K. Le Guin" -> "Guin, Ursula K. Le");
// companies keep their name.
func CreatorSortName(name string, role db.CreditRole) string {
	if role == db.CreditRoleDeveloper || role == db.CreditRolePublisher {
		return name
	}
	i := strings.LastIndexByte(name, ' ')
	if i <= 0 {
		return name
	}
	return name[i+1:] + ", " + name[:i]
}

func editionParams(itemID int64, in EditionInput) db.CatalogCreateEditionParams {
	return db.CatalogCreateEditionParams{
		MediaItemID: itemID, Title: in.Title, Format: in.Format, PlatformID: in.PlatformID, Region: in.Region,
		Publisher: in.Publisher, ReleaseDate: in.ReleaseDate, PageCount: in.PageCount, DurationMinutes: in.DurationMinutes,
	}
}

func CreateEdition(ctx context.Context, q *db.Queries, itemID int64, in EditionInput) (db.Edition, error) {
	return q.CatalogCreateEdition(ctx, editionParams(itemID, in))
}

func UpdateEdition(ctx context.Context, q *db.Queries, id int64, in EditionInput) (db.Edition, error) {
	return q.CatalogUpdateEdition(ctx, db.CatalogUpdateEditionParams{
		ID: id, Title: in.Title, Format: in.Format, PlatformID: in.PlatformID, Region: in.Region,
		Publisher: in.Publisher, ReleaseDate: in.ReleaseDate, PageCount: in.PageCount, DurationMinutes: in.DurationMinutes,
	})
}

// PlatformMatches returns platforms for the picker: an exact name/alias match first, then
// substring matches. An empty query returns every platform.
func PlatformMatches(ctx context.Context, q *db.Queries, query string) ([]db.Platform, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return q.ListPlatforms(ctx)
	}
	var out []db.Platform
	exact, err := q.FindPlatformByName(ctx, query)
	switch {
	case err == nil:
		out = append(out, exact)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	rest, err := q.SearchPlatforms(ctx, query)
	if err != nil {
		return nil, err
	}
	for _, p := range rest {
		if len(out) == 0 || p.ID != out[0].ID {
			out = append(out, p)
		}
	}
	return out, nil
}

func CanEdit(viewer db.User, item db.MediaItem) bool {
	return viewer.Role == db.UserRoleAdmin || (item.CreatedByUserID != nil && *item.CreatedByUserID == viewer.ID)
}

func normalizeGenres(raw string) []string {
	out := []string{}
	for g := range strings.SplitSeq(raw, ",") {
		g = strings.ToLower(strings.Join(strings.Fields(g), " "))
		if g != "" && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}
