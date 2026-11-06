// Package audiobookshelf syncs listening progress from the instance's
// Audiobookshelf server. Each user links their own API key; the server URL is
// admin-configured (OPENSHELF_AUDIOBOOKSHELF_URL), never user-supplied.
package audiobookshelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/secrets"
	"github.com/casmith/openshelf/internal/sync"
)

const (
	Provider = "audiobookshelf"
	// ExternalIDProvider is the external_ids namespace for Audiobookshelf library item IDs.
	ExternalIDProvider = "audiobookshelf_item"

	fetchConcurrency = 4
)

// ErrUnauthorized means Audiobookshelf rejected the API key.
var ErrUnauthorized = errors.New("Audiobookshelf rejected the API key; link the account again with a new key") //nolint:staticcheck // shown to users

type Adapter struct {
	BaseURL string
	HTTP    *providers.HTTP
	Secrets *secrets.Box
	// Metadata is the provider registry, for resolving books to Open Library. Set by internal/app.
	Metadata *providers.Registry
}

var (
	_ sync.Adapter  = (*Adapter)(nil)
	_ sync.Resolver = (*Adapter)(nil)
)

func New(cfg config.Config, box *secrets.Box) *Adapter {
	return &Adapter{BaseURL: cfg.AudiobookshelfURL, HTTP: providers.NewHTTP(20, 20), Secrets: box}
}

func (a *Adapter) Provider() string { return Provider }

func (a *Adapter) Configured() bool { return a.BaseURL != "" && a.Secrets != nil }

func (a *Adapter) Capabilities() sync.Capabilities {
	return sync.Capabilities{
		DisplayName: "Audiobookshelf",
		LinkPath:    "/connections/audiobookshelf/link",
		Audiobooks:  true,
		Progress:    true,
	}
}

// User is GET /api/me.
type User struct {
	ID            string          `json:"id"`
	Username      string          `json:"username"`
	MediaProgress []MediaProgress `json:"mediaProgress"`
}

// MediaProgress times are epoch milliseconds; Duration and CurrentTime are seconds.
type MediaProgress struct {
	LibraryItemID string   `json:"libraryItemId"`
	EpisodeID     *string  `json:"episodeId"`
	Duration      float64  `json:"duration"`
	Progress      float64  `json:"progress"`
	CurrentTime   float64  `json:"currentTime"`
	IsFinished    bool     `json:"isFinished"`
	StartedAt     *float64 `json:"startedAt"`
	FinishedAt    *float64 `json:"finishedAt"`
	LastUpdate    *float64 `json:"lastUpdate"`
}

func (m MediaProgress) started() bool {
	return m.Progress > 0 || m.CurrentTime > 0 || m.IsFinished
}

// LibraryItem is GET /api/items/{id}?expanded=1.
type LibraryItem struct {
	ID        string `json:"id"`
	MediaType string `json:"mediaType"`
	Media     struct {
		Duration float64      `json:"duration"`
		Metadata BookMetadata `json:"metadata"`
	} `json:"media"`
}

type BookMetadata struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Authors  []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Narrators     []string   `json:"narrators"`
	Publisher     string     `json:"publisher"`
	ISBN          string     `json:"isbn"`
	ASIN          string     `json:"asin"`
	PublishedYear flexString `json:"publishedYear"`
	Series        []struct {
		Name     string     `json:"name"`
		Sequence flexString `json:"sequence"`
	} `json:"series"`
	Genres []string `json:"genres"`
}

// flexString accepts a JSON string, number, or null.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	*f = ""
	return nil
}

func (a *Adapter) get(ctx context.Context, key, path string, out any) error {
	h := http.Header{"Authorization": {"Bearer " + key}}
	err := a.HTTP.GetJSON(ctx, strings.TrimRight(a.BaseURL, "/")+path, h, out)
	var se *providers.StatusError
	if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
		return ErrUnauthorized
	}
	if err != nil && !errors.Is(err, providers.ErrNotFound) && ctx.Err() == nil {
		return fmt.Errorf("audiobookshelf %s: %w", path, err)
	}
	return err
}

// Me returns the user that owns key.
func (a *Adapter) Me(ctx context.Context, key string) (*User, error) {
	var u User
	if err := a.get(ctx, key, "/api/me", &u); err != nil {
		if errors.Is(err, providers.ErrNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if u.ID == "" {
		return nil, errors.New("audiobookshelf /api/me: no user in response")
	}
	return &u, nil
}

// Cursor records the newest progress update seen (epoch ms), for diagnostics.
type Cursor struct {
	LastUpdate int64 `json:"last_update"`
}

// Sync re-emits every started or finished book each run: progress is cheap to
// re-apply and the runner is idempotent, so skipped items retry naturally.
func (a *Adapter) Sync(ctx context.Context, acct db.ConnectedAccount, cursor []byte, emit sync.Emitter) ([]byte, error) {
	var cur Cursor
	if len(cursor) > 0 {
		_ = json.Unmarshal(cursor, &cur)
	}
	if len(acct.Credentials) == 0 {
		return nil, ErrUnauthorized
	}
	key, err := a.Secrets.Decrypt(acct.Credentials)
	if err != nil {
		return nil, fmt.Errorf("couldn't read the stored Audiobookshelf key; link the account again: %w", err)
	}
	me, err := a.Me(ctx, string(key))
	if err != nil {
		return nil, err
	}

	var todo []MediaProgress
	seen := map[string]bool{}
	for _, mp := range me.MediaProgress {
		if mp.LastUpdate != nil {
			cur.LastUpdate = max(cur.LastUpdate, int64(*mp.LastUpdate))
		}
		if (mp.EpisodeID != nil && *mp.EpisodeID != "") || mp.LibraryItemID == "" || !mp.started() || seen[mp.LibraryItemID] {
			continue
		}
		seen[mp.LibraryItemID] = true
		todo = append(todo, mp)
	}

	items, errs := a.fetchItems(ctx, string(key), todo)
	missing := 0
	for i, mp := range todo {
		switch err := errs[i]; {
		case errors.Is(err, ErrUnauthorized):
			return nil, err
		case errors.Is(err, providers.ErrNotFound):
			missing++
			continue
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			emit.Warn(fmt.Sprintf("Couldn't read Audiobookshelf item %s; will retry next sync: %v", mp.LibraryItemID, err))
			continue
		}
		it := items[i]
		if it.MediaType != "book" {
			continue
		}
		if err := emit.OwnedItem(ctx, toOwnedItem(mp, it)); err != nil {
			return nil, err
		}
	}
	if missing > 0 {
		emit.Warn(fmt.Sprintf("Skipped %d listening %s for items no longer in Audiobookshelf", missing, plural(missing, "entry", "entries")))
	}
	return json.Marshal(cur)
}

func (a *Adapter) fetchItems(ctx context.Context, key string, todo []MediaProgress) ([]LibraryItem, []error) {
	items := make([]LibraryItem, len(todo))
	errs := make([]error, len(todo))
	sem := make(chan struct{}, fetchConcurrency)
	var wg stdsync.WaitGroup
	for i, mp := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = a.get(ctx, key, "/api/items/"+url.PathEscape(mp.LibraryItemID)+"?expanded=1", &items[i])
		}()
	}
	wg.Wait()
	return items, errs
}

func toOwnedItem(mp MediaProgress, it LibraryItem) sync.OwnedItem {
	md := it.Media.Metadata
	g := sync.OwnedItem{
		Kind:  db.MediaKindBook,
		Key:   mp.LibraryItemID,
		Title: strings.TrimSpace(md.Title),
		ExternalIDs: []providers.ExternalID{{
			Provider: ExternalIDProvider, Value: mp.LibraryItemID,
		}},
		Book: &providers.BookDetails{},
	}
	for _, au := range md.Authors {
		g.Credits = append(g.Credits, providers.Credit{Name: strings.TrimSpace(au.Name), Role: db.CreditRoleAuthor})
	}
	for _, n := range md.Narrators {
		g.Credits = append(g.Credits, providers.Credit{Name: strings.TrimSpace(n), Role: db.CreditRoleNarrator})
	}
	if len(md.Series) > 0 {
		g.Book.SeriesName = strings.TrimSpace(md.Series[0].Name)
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(md.Series[0].Sequence)), 64); err == nil && f >= 0 && f < 10000 {
			g.Book.SeriesPosition = &f
		}
	}
	if y, err := strconv.Atoi(strings.TrimSpace(string(md.PublishedYear))); err == nil && y > 0 && y < 3000 {
		g.ReleaseYear = y
	}

	duration := mp.Duration
	if duration <= 0 {
		duration = it.Media.Duration
	}
	ed := &providers.Edition{Format: db.EditionFormatAudiobook, Publisher: strings.TrimSpace(md.Publisher), ExternalIDs: isbnIDs(md.ISBN)}
	if duration > 0 {
		ed.DurationMinutes = ptr(clamp32(math.Round(duration / 60)))
	}
	g.Edition = ed

	p := &sync.Progress{
		Fraction:       mp.Progress,
		Finished:       mp.IsFinished,
		StartedAt:      msTime(mp.StartedAt),
		FinishedAt:     msTime(mp.FinishedAt),
		LastActivityAt: msTime(mp.LastUpdate),
	}
	if mp.CurrentTime > 0 {
		p.CurrentSeconds = ptr(clamp32(math.Round(mp.CurrentTime)))
	}
	if duration > 0 {
		p.DurationSeconds = ptr(clamp32(math.Round(duration)))
	}
	if !p.Finished {
		p.FinishedAt = nil
	}
	g.Progress = p
	return g
}

// isbnIDs returns isbn13 and isbn10 external IDs for a valid ISBN; an ISBN-10 also yields its ISBN-13.
func isbnIDs(raw string) []providers.ExternalID {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(raw)))
	switch {
	case validISBN13(s):
		return []providers.ExternalID{{Provider: "isbn13", Value: s}}
	case validISBN10(s):
		return []providers.ExternalID{{Provider: "isbn13", Value: isbn10to13(s)}, {Provider: "isbn10", Value: s}}
	}
	return nil
}

func validISBN13(s string) bool {
	if len(s) != 13 {
		return false
	}
	sum := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return sum%10 == 0
}

func validISBN10(s string) bool {
	if len(s) != 10 {
		return false
	}
	sum := 0
	for i, r := range s {
		var d int
		switch {
		case r >= '0' && r <= '9':
			d = int(r - '0')
		case r == 'X' && i == 9:
			d = 10
		default:
			return false
		}
		sum += d * (10 - i)
	}
	return sum%11 == 0
}

func isbn10to13(s string) string {
	body := "978" + s[:9]
	sum := 0
	for i, r := range body {
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return body + strconv.Itoa((10-sum%10)%10)
}

func msTime(ms *float64) *time.Time {
	if ms == nil || *ms <= 0 {
		return nil
	}
	t := time.UnixMilli(int64(*ms)).UTC()
	return &t
}

func clamp32(f float64) int32 {
	switch {
	case f < 0:
		return 0
	case f > math.MaxInt32:
		return math.MaxInt32
	}
	return int32(f)
}

func ptr[T any](v T) *T { return &v }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
