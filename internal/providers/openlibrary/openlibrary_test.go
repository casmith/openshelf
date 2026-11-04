package openlibrary_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
	"github.com/casmith/openshelf/internal/providers/providertest"
)

type fixtureServer struct {
	*httptest.Server
	mu     sync.Mutex
	agents []string
	paths  []string
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	routes := map[string]string{
		"/search.json":                  "search.json",
		"/works/OL59800W.json":          "work-OL59800W.json",
		"/works/OL59800W/editions.json": "editions-OL59800W.json",
		"/authors/OL31353A.json":        "author-OL31353A.json",
	}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.agents = append(fs.agents, r.UserAgent())
		fs.paths = append(fs.paths, r.URL.RequestURI())
		fs.mu.Unlock()
		file, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, filepath.Join("testdata", file))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func TestConformance(t *testing.T) {
	srv := newFixtureServer(t)
	providertest.Conformance(t, openlibrary.NewWithURL(srv.URL), []providertest.Case{
		{Kind: db.MediaKindBook, Query: "the left hand of darkness", WantTitle: "The Left Hand of Darkness"},
	})
	for _, ua := range srv.agents {
		if ua != providers.UserAgent {
			t.Errorf("User-Agent = %q, want contact agent", ua)
		}
	}
	if !strings.Contains(srv.paths[0], "fields=key%2Ctitle%2Cauthor_name%2Cfirst_publish_year%2Ccover_i%2Cisbn") {
		t.Errorf("search request %q lacks fields", srv.paths[0])
	}
}

func TestSearchAndFetchNormalize(t *testing.T) {
	srv := newFixtureServer(t)
	p := openlibrary.NewWithURL(srv.URL)
	ctx := context.Background()

	if !p.Configured() {
		t.Error("Open Library needs no key and is always configured")
	}
	res, err := p.Search(ctx, db.MediaKindBook, "the left hand of darkness")
	if err != nil {
		t.Fatal(err)
	}
	first := res[0]
	if first.ExternalID.Provider != "openlibrary_work" || first.ExternalID.Value != "OL59800W" || first.Year != 1969 ||
		first.Subtitle != "Ursula K. Le Guin" || first.CoverURL != "https://covers.openlibrary.org/b/id/10618463-L.jpg" {
		t.Errorf("first result = %+v", first)
	}
	if other, _ := p.Search(ctx, db.MediaKindFilm, "x"); len(other) != 0 {
		t.Error("films are not supported")
	}

	item, err := p.Fetch(ctx, db.MediaKindBook, "OL59800W")
	if err != nil {
		t.Fatal(err)
	}
	if item.ReleaseYear != 1969 || item.ReleaseDate != nil || item.Synopsis == "" || item.Book == nil {
		t.Errorf("item = %+v", item)
	}
	if item.CoverURL != "https://covers.openlibrary.org/b/id/10618463-L.jpg" {
		t.Errorf("cover = %q", item.CoverURL)
	}
	if len(item.Credits) != 1 || item.Credits[0].Name != "Ursula K. Le Guin" || item.Credits[0].Role != db.CreditRoleAuthor {
		t.Errorf("credits = %+v", item.Credits)
	}
	if len(item.Genres) == 0 || len(item.Genres) > 5 {
		t.Errorf("genres = %v", item.Genres)
	}
	for _, g := range item.Genres {
		if strings.Contains(g, ":") || g != strings.ToLower(g) {
			t.Errorf("genre %q not normalized", g)
		}
	}
	if len(item.Editions) != 6 {
		t.Fatalf("editions = %d", len(item.Editions))
	}
	var isbns []string
	for _, e := range item.Editions {
		for _, x := range e.ExternalIDs {
			if x.Provider == "isbn13" {
				isbns = append(isbns, x.Value)
			}
		}
	}
	if len(isbns) == 0 || isbns[0] != "9781399638319" {
		t.Errorf("isbn13s = %v", isbns)
	}
	fr := item.Editions[1]
	if fr.Title != "La Main Gauche De La Nuit" || fr.Format != db.EditionFormatPaperback || fr.Publisher != "Editions Robert Laffont" {
		t.Errorf("french edition = %+v", fr)
	}
	if item.Editions[3].PageCount == nil || *item.Editions[3].PageCount != 322 {
		t.Errorf("page count missing: %+v", item.Editions[3])
	}

	if _, err := p.Fetch(ctx, db.MediaKindBook, "OL1W"); !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("missing work: %v", err)
	}
	if _, err := p.Fetch(ctx, db.MediaKindBook, "../etc"); !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}
