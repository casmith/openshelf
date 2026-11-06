package audiobookshelf_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/audiobookshelf"
	"github.com/casmith/openshelf/internal/testutil"
)

const (
	aliceKey   = "abs-key-alice-0123456789"
	aliceABSID = "c2f1a9e0-0000-4000-8000-000000000001"
	bobKey     = "abs-key-bob-0123456789"
	bobABSID   = "c2f1a9e0-0000-4000-8000-000000000002"

	itemHailMary = "li_hailmary"
	itemKings    = "li_kings"
	itemUnread   = "li_unread"
	itemPodcast  = "li_podcast"
	itemGone     = "li_gone"
)

var (
	lastListened = time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	startedAt    = time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	finishedAt   = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
)

// FakeABS serves GET /api/me, /api/libraries and /api/libraries/{id}/items like
// Audiobookshelf 2.x (list items are always minified).
type FakeABS struct {
	*httptest.Server
	mu        stdsync.Mutex
	Users     map[string]string // API key -> /api/me body
	Libraries []*FakeLibrary
	Status    int // when set, every request answers with it
	Hits      map[string]int
	Queries   []string // raw queries of item list requests
}

type FakeLibrary struct {
	ID, MediaType string
	Items         []string // minified library item bodies, in addedAt order
}

func newFakeABS(t *testing.T) *FakeABS {
	t.Helper()
	f := &FakeABS{
		Users: map[string]string{
			aliceKey: meBody(aliceABSID, "alice_abs", aliceProgress(0.4333, 18720, false)),
			bobKey:   meBody(bobABSID, "bob_abs", ""),
		},
		Hits: map[string]int{},
	}
	f.Libraries = []*FakeLibrary{
		{ID: "lib_pod", MediaType: "podcast", Items: []string{
			`{"id":"li_podcast","libraryId":"lib_pod","mediaType":"podcast","media":{"metadata":{"title":"A Podcast"},"numEpisodes":3}}`,
		}},
		{ID: "lib_books", MediaType: "book", Items: []string{
			bookBody(itemHailMary, "Project Hail Mary", "Andy Weir", "Ray Porter", "978-0-593-13520-4", `"2021"`, "", 43200),
			bookBody(itemKings, "The Way of Kings", "Brandon Sanderson", "Michael Kramer, Kate Reading", "", `"2010"`, "The Stormlight Archive #1", 165600),
			bookBody(itemUnread, "Unread Book", "", "", "", `null`, "", 3600),
		}},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *FakeABS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Hits[r.URL.Path]++
	if f.Status != 0 {
		http.Error(w, http.StatusText(f.Status), f.Status)
		return
	}
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	me, known := f.Users[key]
	if !ok || !known {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/me" {
		fmt.Fprint(w, me)
		return
	}
	if r.URL.Path == "/api/libraries" {
		var libs []string
		for _, l := range f.Libraries {
			libs = append(libs, fmt.Sprintf(`{"id":%q,"name":"Library %s","mediaType":%q,"provider":"audible"}`, l.ID, l.ID, l.MediaType))
		}
		fmt.Fprintf(w, `{"libraries":[%s]}`, strings.Join(libs, ","))
		return
	}
	for _, l := range f.Libraries {
		if r.URL.Path != "/api/libraries/"+l.ID+"/items" {
			continue
		}
		f.Queries = append(f.Queries, r.URL.RawQuery)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := l.Items
		if limit > 0 {
			start := min(page*limit, len(items))
			items = items[start:min(start+limit, len(items))]
		}
		fmt.Fprintf(w, `{"results":[%s],"total":%d,"limit":%d,"page":%d,"mediaType":%q,"minified":false,"collapseseries":false,"include":"","offset":%d}`,
			strings.Join(items, ","), len(l.Items), limit, page, l.MediaType, page*limit)
		return
	}
	http.NotFound(w, r)
}

func (f *FakeABS) addBooks(lib int, bodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Libraries[lib].Items = append(f.Libraries[lib].Items, bodies...)
}

func (f *FakeABS) setUser(key, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Users[key] = body
}

func (f *FakeABS) hits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Hits[path]
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func meBody(id, username, progress string) string {
	return fmt.Sprintf(`{"id":%q,"username":%q,"type":"user","isActive":true,"mediaProgress":[%s],"bookmarks":[]}`, id, username, progress)
}

func progressEntry(itemID string, episodeID string, duration, progress, current float64, finished bool, started, finishedAtMs, lastUpdate int64) string {
	ep := "null"
	if episodeID != "" {
		ep = fmt.Sprintf("%q", episodeID)
	}
	typ := "book"
	if itemID == itemPodcast {
		typ = "podcastEpisode"
	}
	fin := "null"
	if finishedAtMs > 0 {
		fin = fmt.Sprint(finishedAtMs)
	}
	return fmt.Sprintf(`{"id":"mp_%s","libraryItemId":%q,"episodeId":%s,"mediaItemId":"m_%s","mediaItemType":%q,"duration":%g,"progress":%g,"currentTime":%g,"isFinished":%t,"hideFromContinueListening":false,"ebookLocation":null,"ebookProgress":0,"lastUpdate":%d,"startedAt":%d,"finishedAt":%s}`,
		itemID, itemID, ep, itemID, typ, duration, progress, current, finished, lastUpdate, started, fin)
}

// aliceProgress is Hail Mary in progress, Kings finished, an untouched book, a podcast
// episode, a podcast item, and progress for an item no longer on the server.
func aliceProgress(hailMary, current float64, hailMaryFinished bool) string {
	var finAt int64
	if hailMaryFinished {
		finAt = ms(lastListened)
	}
	return strings.Join([]string{
		progressEntry(itemHailMary, "", 43200.5, hailMary, current, hailMaryFinished, ms(startedAt), finAt, ms(lastListened)),
		progressEntry(itemKings, "", 165600, 1, 165600, true, ms(startedAt.AddDate(0, -3, 0)), ms(finishedAt), ms(finishedAt)),
		progressEntry(itemUnread, "", 3600, 0, 0, false, ms(startedAt), 0, ms(startedAt)),
		progressEntry(itemPodcast, "ep_1", 1800, 0.5, 900, false, ms(startedAt), 0, ms(startedAt)),
		progressEntry(itemPodcast, "", 1800, 0.5, 900, false, ms(startedAt), 0, ms(startedAt)),
		progressEntry(itemGone, "", 1800, 0.2, 360, false, ms(startedAt), 0, ms(startedAt)),
	}, ",")
}

// bookBody is a minified library item; authors, narrators and series are
// comma-joined the way Audiobookshelf's list endpoint returns them.
func bookBody(id, title, authors, narrators, isbn, year, series string, duration float64) string {
	return fmt.Sprintf(`{"id":%q,"ino":"1","libraryId":"lib_books","folderId":"fol1","path":"/audiobooks/x","relPath":"x","isFile":false,"mtimeMs":1,"ctimeMs":1,"birthtimeMs":1,"addedAt":1,"updatedAt":1,"isMissing":false,"isInvalid":false,"mediaType":"book","media":{"id":"m_%s","metadata":{"title":%q,"titleIgnorePrefix":%q,"subtitle":null,"authorName":%q,"authorNameLF":"","narratorName":%q,"seriesName":%q,"genres":["Science Fiction"],"publishedYear":%s,"publishedDate":null,"publisher":"Audible Studios","description":"","isbn":%q,"asin":"B08G9PRS1K","language":"English","explicit":false,"abridged":false},"coverPath":"/metadata/items/%s/cover.jpg","tags":[],"numTracks":1,"numAudioFiles":1,"numChapters":10,"duration":%g,"size":1000},"numFiles":2,"size":1000}`,
		id, id, title, title, authors, narrators, series, year, isbn, id, duration)
}

// FakeOL serves the Open Library endpoints the resolver uses. It knows Project
// Hail Mary by ISBN, nothing that matches The Way of Kings exactly, and Pandora's
// Star by title and author once KnowsPandora is set.
type FakeOL struct {
	*httptest.Server
	mu           stdsync.Mutex
	KnowsPandora bool
	Lookups      int // ISBN and search requests
}

func newFakeOL(t *testing.T) *FakeOL {
	t.Helper()
	routes := map[string]string{
		"/isbn/9780593135204.json":  `{"key":"/books/OL1M","works":[{"key":"/works/OL1W"}]}`,
		"/works/OL1W.json":          `{"key":"/works/OL1W","title":"Project Hail Mary","first_publish_date":"2021","authors":[{"author":{"key":"/authors/OL1A"}}],"covers":[]}`,
		"/authors/OL1A.json":        `{"name":"Andy Weir"}`,
		"/works/OL1W/editions.json": `{"entries":[{"key":"/books/OL1M","publishers":["Ballantine"],"isbn_13":["9780593135204"],"physical_format":"Hardcover"}]}`,
		"/works/OL2W.json":          `{"key":"/works/OL2W","title":"Pandora's Star","first_publish_date":"2004","authors":[{"author":{"key":"/authors/OL2A"}}],"covers":[]}`,
		"/authors/OL2A.json":        `{"name":"Peter F. Hamilton"}`,
		"/works/OL2W/editions.json": `{"entries":[]}`,
	}
	f := &FakeOL{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/search.json" || strings.HasPrefix(r.URL.Path, "/isbn/") {
			f.Lookups++
		}
		if r.URL.Path == "/search.json" {
			if f.KnowsPandora && r.URL.Query().Get("title") == "Pandora's Star" {
				fmt.Fprint(w, `{"docs":[{"key":"/works/OL2W","title":"Pandora's Star","author_name":["Peter F. Hamilton"]}]}`)
				return
			}
			fmt.Fprint(w, `{"docs":[{"key":"/works/OL9W","title":"The Way of Kings: Part One","author_name":["Brandon Sanderson"]}]}`)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *FakeOL) set(knowsPandora bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KnowsPandora = knowsPandora
}

func (f *FakeOL) lookups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Lookups
}

type env struct {
	s   *testutil.Server
	abs *FakeABS
	ol  *FakeOL
	ad  *audiobookshelf.Adapter
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := testutil.NewServer(t)
	f := newFakeABS(t)
	ol := newFakeOL(t)
	h := providers.NewHTTP(1000, 1000)
	h.MaxRetries = 0
	ad := &audiobookshelf.Adapter{
		BaseURL: f.URL, HTTP: h, Secrets: s.Deps.Secrets,
		Metadata: providers.NewRegistry(openlibrary.NewWithHTTP(ol.URL, h)),
	}
	s.Deps.SyncAdapters = sync.NewRegistry(ad)
	return &env{s: s, abs: f, ol: ol, ad: ad}
}

// linkAccount stores an encrypted key the way the link handler does.
func (e *env) linkAccount(t *testing.T, u db.User, absID, key string) db.ConnectedAccount {
	t.Helper()
	acct := testutil.CreateConnectedAccount(t, e.s.Pool, u.ID, audiobookshelf.Provider, absID)
	creds, err := e.s.Deps.Secrets.Encrypt([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool.Exec(context.Background(), `update connected_accounts set credentials = $2 where id = $1`, acct.ID, creds); err != nil {
		t.Fatal(err)
	}
	acct.Credentials = creds
	return acct
}

func scalar[T any](t *testing.T, s *testutil.Server, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

func text(html string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(tagRe.ReplaceAllString(html, " "), " "))
}
