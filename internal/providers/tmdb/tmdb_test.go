package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
)

const token = "test-token"

type fixtureServer struct {
	*httptest.Server
	configHits atomic.Int32
	throttle   atomic.Int32 // number of upcoming requests to answer with 429
	throttled  atomic.Int32
}

func newServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	files := map[string]string{
		"/configuration": "configuration.json",
		"/search/movie":  "search_movie.json",
		"/search/tv":     "search_tv.json",
		"/movie/78":      "movie_78.json",
		"/tv/63639":      "tv_63639.json",
	}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"status_code":7,"status_message":"Invalid API key"}`, http.StatusUnauthorized)
			return
		}
		if fs.throttle.Load() > 0 {
			fs.throttle.Add(-1)
			fs.throttled.Add(1)
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"status_code":25,"status_message":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		name, ok := files[r.URL.Path]
		if !ok {
			http.Error(w, `{"status_code":34,"status_message":"not found"}`, http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/configuration":
			fs.configHits.Add(1)
		case "/movie/78", "/tv/63639":
			if r.URL.Query().Get("append_to_response") != "credits,external_ids" {
				t.Errorf("%s: append_to_response = %q", r.URL.Path, r.URL.Query().Get("append_to_response"))
			}
		default:
			if r.URL.Query().Get("query") == "" {
				t.Errorf("%s: missing query", r.URL.Path)
			}
		}
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func newProvider(t *testing.T) (*Provider, *fixtureServer) {
	fs := newServer(t)
	p := New(config.Config{TMDBToken: token})
	p.BaseURL = fs.URL
	return p, fs
}

func TestConformance(t *testing.T) {
	p, _ := newProvider(t)
	providertest.Conformance(t, p, []providertest.Case{
		{Kind: db.MediaKindFilm, Query: "blade runner", WantTitle: "Blade Runner"},
		{Kind: db.MediaKindShow, Query: "expanse", WantTitle: "The Expanse"},
	})
}

func TestConfigured(t *testing.T) {
	if New(config.Config{}).Configured() {
		t.Error("Configured() = true without a token")
	}
	if !New(config.Config{TMDBToken: token}).Configured() {
		t.Error("Configured() = false with a token")
	}
	if New(config.Config{}).BaseURL != DefaultBaseURL {
		t.Error("unexpected default base URL")
	}
}

func TestSearchMovie(t *testing.T) {
	p, fs := newProvider(t)
	got, err := p.Search(context.Background(), db.MediaKindFilm, "blade runner")
	if err != nil {
		t.Fatal(err)
	}
	want := []providers.SearchResult{
		{
			Provider: "tmdb", Kind: db.MediaKindFilm, Title: "Blade Runner 2049", Year: 2017, Subtitle: "2017",
			ExternalID: providers.ExternalID{Provider: "tmdb_movie", Value: "335984", URL: "https://www.themoviedb.org/movie/335984"},
			CoverURL:   "https://image.tmdb.org/t/p/w500/gajva2L0rPYkEWjzgFlBXCAVBE5.jpg",
		},
		{
			Provider: "tmdb", Kind: db.MediaKindFilm, Title: "Blade Runner", Year: 1982, Subtitle: "1982",
			ExternalID: providers.ExternalID{Provider: "tmdb_movie", Value: "78", URL: "https://www.themoviedb.org/movie/78"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search =\n%+v\nwant\n%+v", got, want)
	}
	if _, err := p.Search(context.Background(), db.MediaKindFilm, "again"); err != nil {
		t.Fatal(err)
	}
	if n := fs.configHits.Load(); n != 1 {
		t.Errorf("configuration fetched %d times, want 1", n)
	}
}

func TestSearchTVSubtitleUsesOriginalName(t *testing.T) {
	p, _ := newProvider(t)
	got, err := p.Search(context.Background(), db.MediaKindShow, "expanse")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results", len(got))
	}
	if got[0].Year != 2015 || got[0].ExternalID.Provider != "tmdb_tv" || got[0].ExternalID.Value != "63639" {
		t.Errorf("first result = %+v", got[0])
	}
	if got[1].Subtitle != "広がる宇宙" || got[1].Year != 0 || got[1].CoverURL != "" {
		t.Errorf("second result = %+v", got[1])
	}
}

func TestFetchMovie(t *testing.T) {
	p, _ := newProvider(t)
	item, err := p.Fetch(context.Background(), db.MediaKindFilm, "78")
	if err != nil {
		t.Fatal(err)
	}
	providertest.CheckItem(t, item, db.MediaKindFilm)
	if item.Title != "Blade Runner" || item.OriginalTitle != "" || item.ReleaseYear != 1982 ||
		item.ReleaseDate == nil || item.ReleaseDate.Format("2006-01-02") != "1982-06-25" {
		t.Errorf("basic fields: %+v", item)
	}
	if item.CoverURL != "https://image.tmdb.org/t/p/w500/63N9uy8nd9j7Eog2axPQ8lbr3Wj.jpg" {
		t.Errorf("CoverURL = %q", item.CoverURL)
	}
	if item.Film == nil || item.Film.RuntimeMinutes == nil || *item.Film.RuntimeMinutes != 117 {
		t.Errorf("Film = %+v", item.Film)
	}
	if len(item.Editions) != 0 {
		t.Errorf("films should have no editions, got %d", len(item.Editions))
	}
	if !reflect.DeepEqual(item.Genres, []string{"science fiction", "drama", "thriller"}) {
		t.Errorf("Genres = %v", item.Genres)
	}
	wantIDs := []providers.ExternalID{
		{Provider: "tmdb_movie", Value: "78", URL: "https://www.themoviedb.org/movie/78"},
		{Provider: "imdb", Value: "tt0083658", URL: "https://www.imdb.com/title/tt0083658/"},
	}
	if !reflect.DeepEqual(item.ExternalIDs, wantIDs) {
		t.Errorf("ExternalIDs = %+v", item.ExternalIDs)
	}
	wantCredits := []providers.Credit{
		{Name: "Ridley Scott", Role: db.CreditRoleDirector},
		{Name: "Hampton Fancher", Role: db.CreditRoleWriter},
		{Name: "David Peoples", Role: db.CreditRoleWriter},
		{Name: "Harrison Ford", Role: db.CreditRoleActor},
		{Name: "Rutger Hauer", Role: db.CreditRoleActor},
		{Name: "Sean Young", Role: db.CreditRoleActor},
		{Name: "Edward James Olmos", Role: db.CreditRoleActor},
		{Name: "M. Emmet Walsh", Role: db.CreditRoleActor},
	}
	if !reflect.DeepEqual(item.Credits, wantCredits) {
		t.Errorf("Credits = %+v", item.Credits)
	}
	if item.Attribution != Attribution {
		t.Errorf("Attribution = %q", item.Attribution)
	}
}

func TestFetchShow(t *testing.T) {
	p, _ := newProvider(t)
	item, err := p.Fetch(context.Background(), db.MediaKindShow, "63639")
	if err != nil {
		t.Fatal(err)
	}
	providertest.CheckItem(t, item, db.MediaKindShow)
	if item.Title != "The Expanse" || item.ReleaseYear != 2015 {
		t.Errorf("basic fields: %+v", item)
	}
	s := item.Show
	if s == nil || s.Network != "Syfy" || s.Status != "ended" ||
		s.SeasonCount == nil || *s.SeasonCount != 6 || s.EpisodeCount == nil || *s.EpisodeCount != 62 {
		t.Errorf("Show = %+v", s)
	}
	if !reflect.DeepEqual(item.Genres, []string{"drama", "sci-fi & fantasy"}) {
		t.Errorf("Genres = %v", item.Genres)
	}
	if len(item.ExternalIDs) != 2 || item.ExternalIDs[0].Provider != "tmdb_tv" || item.ExternalIDs[1].Value != "tt3230854" {
		t.Errorf("ExternalIDs = %+v", item.ExternalIDs)
	}
	wantCredits := []providers.Credit{
		{Name: "Mark Fergus", Role: db.CreditRoleCreator},
		{Name: "Hawk Ostby", Role: db.CreditRoleCreator},
		{Name: "Steven Strait", Role: db.CreditRoleActor},
		{Name: "Dominique Tipper", Role: db.CreditRoleActor},
		{Name: "Wes Chatham", Role: db.CreditRoleActor},
	}
	if !reflect.DeepEqual(item.Credits, wantCredits) {
		t.Errorf("Credits = %+v", item.Credits)
	}
}

func TestFetchNotFound(t *testing.T) {
	p, _ := newProvider(t)
	for _, id := range []string{"999", "abc"} {
		if _, err := p.Fetch(context.Background(), db.MediaKindFilm, id); !errors.Is(err, providers.ErrNotFound) {
			t.Errorf("Fetch(%q) err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestUnsupportedKind(t *testing.T) {
	p, _ := newProvider(t)
	if _, err := p.Search(context.Background(), db.MediaKindBook, "x"); err == nil {
		t.Error("expected error for book search")
	}
}

func TestRetriesOn429(t *testing.T) {
	p, fs := newProvider(t)
	fs.throttle.Store(2)
	item, err := p.Fetch(context.Background(), db.MediaKindFilm, "78")
	if err != nil {
		t.Fatalf("Fetch after 429s: %v", err)
	}
	if item.Title != "Blade Runner" || fs.throttled.Load() != 2 {
		t.Errorf("title %q, throttled %d", item.Title, fs.throttled.Load())
	}

	fs.throttle.Store(100)
	_, err = p.Search(context.Background(), db.MediaKindFilm, "blade runner")
	var se *providers.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Errorf("persistent 429 err = %v", err)
	}
}

func TestStatusNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"Returning Series": "returning", "Ended": "ended", "Canceled": "canceled",
		"Cancelled": "canceled", "In Production": "in production", "": "",
	} {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
