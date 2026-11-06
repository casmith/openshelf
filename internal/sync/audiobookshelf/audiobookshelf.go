// Package audiobookshelf syncs the user's audiobook library and listening
// progress from the instance's Audiobookshelf server. Each user links their own
// API key; the server URL is admin-configured (OPENSHELF_AUDIOBOOKSHELF_URL), never user-supplied.
package audiobookshelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

	pageSize = 100
	maxPages = 1000
	// DefaultLookupBudget caps Open Library resolutions per run (~1 req/s, a few
	// requests each) so a first sync of a large library fits the job timeout.
	DefaultLookupBudget = 200
)

// ErrUnauthorized means Audiobookshelf rejected the API key.
var ErrUnauthorized = errors.New("Audiobookshelf rejected the API key; link the account again with a new key") //nolint:staticcheck // shown to users

type Adapter struct {
	BaseURL string
	HTTP    *providers.HTTP
	Secrets *secrets.Box
	// Metadata is the provider registry, for resolving books to Open Library. Set by internal/app.
	Metadata *providers.Registry
	// LookupBudget overrides DefaultLookupBudget when positive.
	LookupBudget int
}

func (a *Adapter) lookupBudget() int {
	if a.LookupBudget > 0 {
		return a.LookupBudget
	}
	return DefaultLookupBudget
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
	MediaItemType string   `json:"mediaItemType"` // "book" or "podcastEpisode"
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

// Library is an entry of GET /api/libraries.
type Library struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
}

// ItemsPage is GET /api/libraries/{id}/items. Audiobookshelf always returns
// minified items here: authors, narrators and series are comma-joined strings.
type ItemsPage struct {
	Results []LibraryItem `json:"results"`
	Total   int           `json:"total"`
}

type LibraryItem struct {
	ID        string `json:"id"`
	MediaType string `json:"mediaType"`
	Media     struct {
		Duration float64      `json:"duration"`
		Metadata BookMetadata `json:"metadata"`
	} `json:"media"`
}

type BookMetadata struct {
	Title         string     `json:"title"`
	Subtitle      string     `json:"subtitle"`
	AuthorName    string     `json:"authorName"`
	NarratorName  string     `json:"narratorName"`
	SeriesName    string     `json:"seriesName"`
	Publisher     string     `json:"publisher"`
	ISBN          string     `json:"isbn"`
	ASIN          string     `json:"asin"`
	PublishedYear flexString `json:"publishedYear"`
	Genres        []string   `json:"genres"`
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

// Sync emits every book in the libraries the key can access, with progress
// attached where the user has any. The runner is idempotent and matches imported
// items by their audiobookshelf_item ID, so re-emitting the whole library is cheap.
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

	progress := map[string]MediaProgress{}
	for _, mp := range me.MediaProgress {
		if mp.LastUpdate != nil {
			cur.LastUpdate = max(cur.LastUpdate, int64(*mp.LastUpdate))
		}
		if (mp.EpisodeID != nil && *mp.EpisodeID != "") || (mp.MediaItemType != "" && mp.MediaItemType != "book") ||
			mp.LibraryItemID == "" || !mp.started() {
			continue
		}
		progress[mp.LibraryItemID] = mp
	}

	libs, err := a.Libraries(ctx, string(key))
	if err != nil {
		return nil, err
	}
	ctx = WithLookupBudget(ctx, a.lookupBudget())
	seen := map[string]bool{}
	for _, lib := range libs {
		if lib.MediaType != "book" {
			continue
		}
		err := a.eachItem(ctx, string(key), lib.ID, func(it LibraryItem) error {
			if it.ID == "" || it.MediaType != "book" || seen[it.ID] {
				return nil
			}
			seen[it.ID] = true
			var mp *MediaProgress
			if p, ok := progress[it.ID]; ok {
				mp = &p
			}
			return emit.OwnedItem(ctx, toOwnedItem(it, mp))
		})
		if err != nil && !errors.Is(err, providers.ErrNotFound) {
			return nil, err
		}
	}
	missing := 0
	for id := range progress {
		if !seen[id] {
			missing++
		}
	}
	if missing > 0 {
		emit.Warn(fmt.Sprintf("Skipped %d listening %s for items no longer in Audiobookshelf", missing, plural(missing, "entry", "entries")))
	}
	return json.Marshal(cur)
}

// Libraries returns the libraries key can access.
func (a *Adapter) Libraries(ctx context.Context, key string) ([]Library, error) {
	var res struct {
		Libraries []Library `json:"libraries"`
	}
	if err := a.get(ctx, key, "/api/libraries", &res); err != nil {
		return nil, err
	}
	return res.Libraries, nil
}

// eachItem pages through a library oldest first, so items added mid-sync land on later pages.
func (a *Adapter) eachItem(ctx context.Context, key, libraryID string, fn func(LibraryItem) error) error {
	seen := 0
	for page := 0; page < maxPages; page++ {
		v := url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}, "sort": {"addedAt"}, "minified": {"1"}}
		var res ItemsPage
		if err := a.get(ctx, key, "/api/libraries/"+url.PathEscape(libraryID)+"/items?"+v.Encode(), &res); err != nil {
			return err
		}
		for _, it := range res.Results {
			if err := fn(it); err != nil {
				return err
			}
		}
		seen += len(res.Results)
		if len(res.Results) < pageSize || seen >= res.Total {
			return nil
		}
	}
	return nil
}

func toOwnedItem(it LibraryItem, mp *MediaProgress) sync.OwnedItem {
	md := it.Media.Metadata
	g := sync.OwnedItem{
		Kind:  db.MediaKindBook,
		Key:   it.ID,
		Title: CleanTitle(md.Title),
		ExternalIDs: []providers.ExternalID{{
			Provider: ExternalIDProvider, Value: it.ID,
		}},
		Book: &providers.BookDetails{},
	}
	for _, n := range splitNames(md.AuthorName) {
		g.Credits = append(g.Credits, providers.Credit{Name: n, Role: db.CreditRoleAuthor})
	}
	for _, n := range splitNames(md.NarratorName) {
		g.Credits = append(g.Credits, providers.Credit{Name: n, Role: db.CreditRoleNarrator})
	}
	if name, seq := firstSeries(md.SeriesName); name != "" {
		g.Book.SeriesName = name
		if f, err := strconv.ParseFloat(seq, 64); err == nil && f >= 0 && f < 10000 {
			g.Book.SeriesPosition = &f
		}
	}
	if y, err := strconv.Atoi(strings.TrimSpace(string(md.PublishedYear))); err == nil && y > 0 && y < 3000 {
		g.ReleaseYear = y
	}

	duration := it.Media.Duration
	if mp != nil && mp.Duration > 0 {
		duration = mp.Duration
	}
	ed := &providers.Edition{Format: db.EditionFormatAudiobook, Publisher: strings.TrimSpace(md.Publisher), ExternalIDs: isbnIDs(md.ISBN)}
	if duration > 0 {
		ed.DurationMinutes = ptr(clamp32(math.Round(duration / 60)))
	}
	g.Edition = ed
	if mp == nil {
		return g
	}

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

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

var seriesRe = regexp.MustCompile(`^(.+?)(?: #([^,]*))?(?:, |$)`)

// firstSeries parses the first entry of a minified seriesName ("Name #1, Other #2").
func firstSeries(s string) (name, seq string) {
	m := seriesRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
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
