package igdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
)

type fakeIGDB struct {
	t         *testing.T
	mu        sync.Mutex
	tokens    int
	revoked   map[string]bool
	expiresIn int
	bodies    []string
}

func (f *fakeIGDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/token":
		q := r.URL.Query()
		if r.Method != http.MethodPost || q.Get("client_id") != "cid" || q.Get("client_secret") != "secret" || q.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad token request", http.StatusBadRequest)
			return
		}
		f.tokens++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("tok-%d", f.tokens), "expires_in": f.expiresIn, "token_type": "bearer",
		})
	case "/v4/games":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method != http.MethodPost || r.Header.Get("Client-ID") != "cid" || tok == "" || f.revoked[tok] {
			http.Error(w, `{"message":"Authorization Failure"}`, http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		f.bodies = append(f.bodies, body)
		file := ""
		switch {
		case strings.HasPrefix(body, `search "rez";`):
			file = "search_rez.json"
		case strings.Contains(body, "where id = 1072;"):
			file = "game_rez.json"
		case strings.Contains(body, "where id = 231;"):
			file = "game_half_life.json"
		}
		if file == "" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		data, err := os.ReadFile("testdata/" + file)
		if err != nil {
			f.t.Fatal(err)
		}
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func newTest(t *testing.T) (*Provider, *fakeIGDB) {
	t.Helper()
	f := &fakeIGDB{t: t, revoked: map[string]bool{}, expiresIn: 5000000}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := New(config.Config{IGDBClientID: "cid", IGDBClientSecret: "secret"})
	p.APIURL = srv.URL + "/v4"
	p.TokenURL = srv.URL + "/token"
	p.HTTP.Limiter = nil
	return p, f
}

func TestConformance(t *testing.T) {
	p, _ := newTest(t)
	providertest.Conformance(t, p, []providertest.Case{{Kind: db.MediaKindGame, Query: "rez", WantTitle: "Rez"}})
}

func TestSearch(t *testing.T) {
	p, f := newTest(t)
	res, err := p.Search(t.Context(), db.MediaKindGame, "rez")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results", len(res))
	}
	r := res[0]
	if r.ExternalID.Provider != "igdb_game" || r.ExternalID.Value != "1072" || r.Year != 2001 ||
		r.Subtitle != "PlayStation 2, Dreamcast" ||
		r.CoverURL != "https://images.igdb.com/igdb/image/upload/t_cover_big/co1nfn.jpg" {
		t.Errorf("result = %+v", r)
	}
	want := `search "rez"; fields name,first_release_date,cover.image_id,platforms.name; limit 20;`
	if f.bodies[0] != want {
		t.Errorf("body = %q, want %q", f.bodies[0], want)
	}
	if res, _ := p.Search(t.Context(), db.MediaKindBook, "rez"); len(res) != 0 {
		t.Errorf("book search returned %v", res)
	}
}

func TestSearchEscapesQuotes(t *testing.T) {
	p, f := newTest(t)
	if _, err := p.Search(t.Context(), db.MediaKindGame, `say "hi" \ bye`); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.bodies[0], `search "say \"hi\" \\ bye";`) {
		t.Errorf("body = %q", f.bodies[0])
	}
}

func TestFetchDreamcastAndPS2Editions(t *testing.T) {
	p, _ := newTest(t)
	item, err := p.Fetch(t.Context(), db.MediaKindGame, "1072")
	if err != nil {
		t.Fatal(err)
	}
	providertest.CheckItem(t, item, db.MediaKindGame)
	if len(item.Editions) != 2 {
		t.Fatalf("got %d editions: %+v", len(item.Editions), item.Editions)
	}
	want := map[string]string{"23": "dreamcast", "8": "ps2"}
	for _, e := range item.Editions {
		pl := e.Platform
		if pl == nil || pl.ExternalID == nil || pl.ExternalID.Provider != "igdb_platform" {
			t.Fatalf("edition platform = %+v", pl)
		}
		if want[pl.ExternalID.Value] != pl.Name {
			t.Errorf("platform %s name = %q, want %q", pl.ExternalID.Value, pl.Name, want[pl.ExternalID.Value])
		}
		delete(want, pl.ExternalID.Value)
		if e.Region != "JP" || e.Format != "" {
			t.Errorf("edition region/format = %q/%q", e.Region, e.Format)
		}
		if e.ReleaseDate == nil || !e.ReleaseDate.Equal(time.Date(2001, 11, 22, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("edition date = %v, want earliest 2001-11-22", e.ReleaseDate)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing platforms %v", want)
	}
	if item.Game == nil || item.Game.Franchise != "Rez" {
		t.Errorf("game details = %+v", item.Game)
	}
	if item.Attribution != Attribution || item.ReleaseYear != 2001 || item.ExternalIDs[0].Value != "1072" {
		t.Errorf("item = %+v", item)
	}
	if strings.Join(item.Genres, ",") != "shooter,music" {
		t.Errorf("genres = %v", item.Genres)
	}
	wantCredits := []providers.Credit{
		{Name: "United Game Artists", Role: db.CreditRoleDeveloper},
		{Name: "Sega", Role: db.CreditRolePublisher},
	}
	if fmt.Sprint(item.Credits) != fmt.Sprint(wantCredits) {
		t.Errorf("credits = %v", item.Credits)
	}
}

func TestFetchPCAndUnknownPlatform(t *testing.T) {
	p, _ := newTest(t)
	item, err := p.Fetch(t.Context(), db.MediaKindGame, "231")
	if err != nil {
		t.Fatal(err)
	}
	providertest.CheckItem(t, item, db.MediaKindGame)
	if len(item.Editions) != 3 {
		t.Fatalf("got %d editions: %+v", len(item.Editions), item.Editions)
	}
	pc, ps2, unknown := item.Editions[0], item.Editions[1], item.Editions[2]
	if pc.Platform.Name != "pc" || pc.Format != db.EditionFormatDigital || pc.Region != "" ||
		!pc.ReleaseDate.Equal(time.Date(1998, 11, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("pc edition = %+v", pc)
	}
	if ps2.Platform.Name != "ps2" || ps2.Region != "NTSC-U" || ps2.Format != "" {
		t.Errorf("ps2 edition = %+v", ps2)
	}
	if unknown.Platform.Name != "Imaginary Console" || unknown.Platform.ExternalID.Value != "9999" || unknown.ReleaseDate != nil {
		t.Errorf("unknown edition = %+v", unknown.Platform)
	}
	if item.Game.Franchise != "Half-Life" {
		t.Errorf("franchise = %q", item.Game.Franchise)
	}
	if len(item.Credits) != 3 {
		t.Errorf("credits = %v", item.Credits)
	}
	if len(item.ExternalIDs) != 2 || item.ExternalIDs[1] != (providers.ExternalID{Provider: "steam_app", Value: "70", URL: "https://store.steampowered.com/app/70"}) {
		t.Errorf("external IDs = %v", item.ExternalIDs)
	}
}

func TestFetchNotFound(t *testing.T) {
	p, _ := newTest(t)
	for _, id := range []string{"42", "abc", "-1"} {
		if _, err := p.Fetch(t.Context(), db.MediaKindGame, id); !errors.Is(err, providers.ErrNotFound) {
			t.Errorf("Fetch(%q) err = %v", id, err)
		}
	}
	if _, err := p.Fetch(t.Context(), db.MediaKindFilm, "1072"); !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("film fetch err = %v", err)
	}
}

func TestTokenCachedAndRefreshedWhenExpired(t *testing.T) {
	p, f := newTest(t)
	f.expiresIn = 3600
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	for range 2 {
		if _, err := p.Search(t.Context(), db.MediaKindGame, "rez"); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens != 1 {
		t.Fatalf("token requests = %d, want 1 (cached)", f.tokens)
	}

	now = now.Add(3600*time.Second - 30*time.Second) // inside the 60 s refresh window
	if _, err := p.Search(t.Context(), db.MediaKindGame, "rez"); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 || p.token != "tok-2" {
		t.Fatalf("token requests = %d, token = %q; want refresh", f.tokens, p.token)
	}
}

func TestRetriesOnceOn401(t *testing.T) {
	p, f := newTest(t)
	if _, err := p.Search(t.Context(), db.MediaKindGame, "rez"); err != nil {
		t.Fatal(err)
	}
	f.revoked["tok-1"] = true
	res, err := p.Search(t.Context(), db.MediaKindGame, "rez")
	if err != nil || len(res) != 2 {
		t.Fatalf("Search after revoke: %v, %d results", err, len(res))
	}
	if f.tokens != 2 {
		t.Fatalf("token requests = %d, want 2", f.tokens)
	}

	f.revoked["tok-2"], f.revoked["tok-3"] = true, true
	_, err = p.Search(t.Context(), db.MediaKindGame, "rez")
	var se *providers.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want 401 after one retry", err)
	}
	if f.tokens != 3 {
		t.Fatalf("token requests = %d, want 3 (only one retry)", f.tokens)
	}
}

func TestConfigured(t *testing.T) {
	for _, c := range []config.Config{{}, {IGDBClientID: "cid"}, {IGDBClientSecret: "secret"}} {
		p := New(c)
		if p.Configured() {
			t.Errorf("Configured() with %+v = true", c)
		}
		if _, err := p.Search(t.Context(), db.MediaKindGame, "rez"); err == nil {
			t.Error("unconfigured search should fail")
		}
	}
	if !New(config.Config{IGDBClientID: "cid", IGDBClientSecret: "secret"}).Configured() {
		t.Error("Configured() with credentials = false")
	}
}

func TestPlatformSlugsAreSeeded(t *testing.T) {
	seed, err := os.ReadFile("../../migrations/00052_platforms_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	for id, slug := range platformSlugs {
		if !strings.Contains(string(seed), "('"+slug+"', '") {
			t.Errorf("IGDB platform %d maps to unseeded slug %q", id, slug)
		}
	}
}
