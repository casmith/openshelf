// Package acceptance runs the README's MVP acceptance test end to end over HTTP.
package acceptance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/exchange"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
	"github.com/casmith/openshelf/internal/testutil"
)

var ctx = context.Background()

func want(t *testing.T, res *testutil.Response, status int) *testutil.Response {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, res.Body)
	}
	return res
}

func contains(t *testing.T, res *testutil.Response, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(res.Body, s) {
			t.Errorf("%s %s: missing %q", res.Request.Method, res.Request.URL, s)
		}
	}
}

func lacks(t *testing.T, res *testutil.Response, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(res.Body, s) {
			t.Errorf("%s %s: unexpectedly contains %q", res.Request.Method, res.Request.URL, s)
		}
	}
}

func scalar[T any](t *testing.T, s *testutil.Server, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := s.Pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func filmFake() *providertest.Fake {
	return &providertest.Fake{ProviderName: "tmdb", Kind: db.MediaKindFilm, Items: map[string]*providers.Item{
		"329865": {
			Kind: db.MediaKindFilm, Title: "Arrival", ReleaseYear: 2016, Genres: []string{"Science Fiction"},
			Credits:     []providers.Credit{{Name: "Denis Villeneuve", Role: db.CreditRoleDirector}},
			ExternalIDs: []providers.ExternalID{{Provider: "tmdb_movie", Value: "329865"}},
			Film:        &providers.FilmDetails{},
		},
	}}
}

var hiddenInput = regexp.MustCompile(`<input type="hidden" name="(\w+)" value="([^"]*)"`)

func TestMVPAcceptance(t *testing.T) {
	s := testutil.NewServer(t)
	s.Deps.Providers = providers.NewRegistry(filmFake())
	c := s.Client(t)

	// A fresh instance sends visitors to setup, which creates and signs in the admin.
	res := want(t, c.Get("/login"), http.StatusSeeOther)
	if loc := res.Header.Get("Location"); loc != "/setup" {
		t.Fatalf("/login redirects to %q, want /setup", loc)
	}
	want(t, c.Get("/setup"), http.StatusOK)
	want(t, c.PostForm("/setup", url.Values{"username": {"alice"}, "display_name": {"Alice"},
		"password": {"password123"}, "password_confirm": {"password123"}}), http.StatusOK)
	want(t, c.Get("/"), http.StatusOK)
	want(t, c.Get("/setup"), http.StatusNotFound)
	alice, err := s.Q.GetUserByUsername(ctx, "alice")
	if err != nil || alice.Role != db.UserRoleAdmin {
		t.Fatalf("setup admin: %+v %v", alice, err)
	}

	// Two items by hand, one through Discover.
	create := func(v url.Values) string {
		t.Helper()
		res := want(t, c.PostForm("/items", v), http.StatusSeeOther)
		return res.Header.Get("Location")
	}
	game := create(url.Values{"kind": {"game"}, "title": {"Shenmue"}, "release_year": {"1999"}})
	book := create(url.Values{"kind": {"book"}, "title": {"Dune"}, "release_year": {"1965"}})

	res = want(t, c.Get("/discover?kind=film&q=arrival"), http.StatusOK)
	contains(t, res, "Arrival")
	fields := url.Values{}
	for _, m := range hiddenInput.FindAllStringSubmatch(res.Body, -1) {
		fields.Set(m[1], m[2])
	}
	if fields.Get("provider") != "tmdb" || fields.Get("id") != "329865" {
		t.Fatalf("discover import form fields = %v", fields)
	}
	res = want(t, c.PostForm("/discover/import", fields), http.StatusSeeOther)
	film := res.Header.Get("Location")
	if !strings.HasPrefix(film, "/items/") {
		t.Fatalf("discover import redirected to %q", film)
	}

	// Library: the book is owned but untouched and backlogged; the game is owned; the film is in progress.
	want(t, c.PostForm(book+"/library", url.Values{"ownership": {"owned"}, "backlog": {"on"}, "visibility": {"private"}}), http.StatusSeeOther)
	want(t, c.PostForm(game+"/library", url.Values{"ownership": {"owned"}, "visibility": {"private"}}), http.StatusSeeOther)
	want(t, c.PostForm(film+"/library", url.Values{"ownership": {"borrowed"}, "visibility": {"private"}}), http.StatusSeeOther)

	experience := func(item string) string {
		t.Helper()
		want(t, c.PostForm(item+"/experiences", nil), http.StatusSeeOther)
		id := scalar[uuid.UUID](t, s, `select x.public_id from experiences x join media_items m on m.id = x.media_item_id
			where m.public_id = $1 order by x.id desc limit 1`, uuid.MustParse(strings.TrimPrefix(item, "/items/")))
		return id.String()
	}
	gameExp := experience(game)
	want(t, c.PostForm("/experiences/"+gameExp+"/finish", nil), http.StatusSeeOther)
	experience(film)

	want(t, c.PostForm(game+"/review", url.Values{"rating": {"90"}, "body": {"Forklifts and sailors."},
		"visibility": {"instance"}, "experience": {gameExp}}), http.StatusSeeOther)

	res = want(t, c.Get("/library?status=backlog"), http.StatusOK)
	contains(t, res, "Dune")
	lacks(t, res, "Shenmue", "Arrival")
	res = want(t, c.Get("/library?status=finished"), http.StatusOK)
	contains(t, res, "Shenmue")
	lacks(t, res, "Dune", "Arrival")
	res = want(t, c.Get("/library?status=in_progress"), http.StatusOK)
	contains(t, res, "Arrival")
	lacks(t, res, "Dune", "Shenmue")

	// A list with all three, shared with the instance.
	res = want(t, c.PostForm("/lists", url.Values{"name": {"Favorites"}, "visibility": {"instance"}, "ranked": {"on"}}), http.StatusSeeOther)
	list := res.Header.Get("Location")
	for _, it := range []string{game, book, film} {
		want(t, c.PostForm(list+"/items", url.Values{"item": {strings.TrimPrefix(it, "/items/")}}), http.StatusSeeOther)
	}

	res = want(t, c.PostForm("/admin/users", url.Values{"username": {"bob"}, "display_name": {"Bob"}}), http.StatusOK)
	m := regexp.MustCompile(`data-temp-password>([^<]+)<`).FindStringSubmatch(res.Body)
	if m == nil {
		t.Fatal("no temporary password for bob")
	}
	bc := s.Client(t)
	want(t, bc.PostForm("/login", url.Values{"username": {"bob"}, "password": {m[1]}}), http.StatusSeeOther)
	res = want(t, bc.Get(list), http.StatusOK)
	contains(t, res, "Favorites", "Shenmue", "Dune", "Arrival")
	lacks(t, res, list+"/edit")
	want(t, bc.Get(list+"/edit"), http.StatusNotFound)
	want(t, bc.PostForm(list+"/edit", url.Values{"name": {"Mine now"}, "visibility": {"public"}}), http.StatusNotFound)
	want(t, bc.PostForm(list+"/items/"+strings.TrimPrefix(book, "/items/")+"/delete", nil), http.StatusNotFound)
	contains(t, want(t, c.Get(list), http.StatusOK), "Favorites", "Shenmue", "Dune", "Arrival")
	lacks(t, want(t, bc.Get(game), http.StatusOK), `href="`+game+`/cover"`, `href="`+game+`/edit"`)

	checkPages := func() {
		t.Helper()
		res := want(t, c.Get("/"), http.StatusOK)
		contains(t, res, "Arrival", "Shenmue", "Dune")
		contains(t, want(t, c.Get("/library"), http.StatusOK), "Arrival", "Shenmue", "Dune")
		contains(t, want(t, c.Get("/journal"), http.StatusOK), "Arrival", "Shenmue", "Dune")
		contains(t, want(t, c.Get("/stats?period=all"), http.StatusOK), "Completed by media type")
		contains(t, want(t, c.Get(game), http.StatusOK), "Shenmue", "Forklifts and sailors.")
		contains(t, want(t, c.Get(book), http.StatusOK), "Dune")
		contains(t, want(t, c.Get(film), http.StatusOK), "Arrival", "Denis Villeneuve")
	}
	checkPages()

	// Export, wipe alice's data, import the export, and compare.
	contains(t, want(t, c.Get("/settings"), http.StatusOK), `href="/settings/export"`, `href="/settings/import"`)
	contains(t, want(t, c.Get("/settings/export"), http.StatusOK), `href="/settings/export.json"`)
	before, raw := exportDoc(t, c)
	if len(before.LibraryEntries) != 3 || len(before.Experiences) != 2 || len(before.Reviews) != 1 || len(before.Lists) != 1 {
		t.Fatalf("export has %d entries, %d experiences, %d reviews, %d lists", len(before.LibraryEntries),
			len(before.Experiences), len(before.Reviews), len(before.Lists))
	}
	for _, tbl := range []string{"activity_events", "reviews", "experiences", "library_entries", "lists"} {
		if _, err := s.Pool.Exec(ctx, "delete from "+tbl+" where user_id = $1", alice.ID); err != nil {
			t.Fatal(err)
		}
	}
	lacks(t, want(t, c.Get("/library"), http.StatusOK), "Shenmue")

	res = want(t, c.Multipart("/settings/import", nil, "file", "export.json", raw), http.StatusSeeOther)
	pid := uuid.MustParse(strings.TrimPrefix(res.Header.Get("Location"), "/settings/import/"))
	jobID := scalar[int64](t, s, `select id from import_jobs where public_id = $1`, pid)
	if err := exchange.Process(ctx, s.Deps, exchange.ImportArgs{JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	res = want(t, c.Get("/settings/import/"+pid.String()), http.StatusOK)
	contains(t, res, "Imported")
	if st := scalar[string](t, s, `select status from import_jobs where id = $1`, jobID); st != exchange.StatusDone {
		t.Fatalf("import status %q", st)
	}

	after, _ := exportDoc(t, c)
	if a, b := canonical(after), canonical(before); !slices.Equal(a, b) {
		t.Fatalf("round trip differs:\n got %s\nwant %s", strings.Join(a, "\n     "), strings.Join(b, "\n     "))
	}
	if n := scalar[int](t, s, `select count(*) from media_items`); n != 3 {
		t.Errorf("import duplicated catalog items: %d", n)
	}
	checkPages()
}

func exportDoc(t *testing.T, c *testutil.Client) (exchange.Export, []byte) {
	t.Helper()
	res := want(t, c.Get("/settings/export.json"), http.StatusOK)
	var doc exchange.Export
	if err := json.Unmarshal([]byte(res.Body), &doc); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	return doc, []byte(res.Body)
}

// canonical describes an export by content, ignoring IDs, timestamps, and visibility.
func canonical(doc exchange.Export) []string {
	items := map[uuid.UUID]string{}
	for _, it := range doc.Items {
		items[it.ID] = string(it.Kind) + "/" + it.Title
	}
	exps := map[uuid.UUID]string{}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	var out []string
	for _, e := range doc.LibraryEntries {
		out = append(out, fmt.Sprintf("entry %s own=%s backlog=%v", items[e.Item], *e.Ownership, e.Backlog))
	}
	for _, e := range doc.Experiences {
		exps[e.ID] = items[e.Item] + " " + string(e.State)
		out = append(out, fmt.Sprintf("exp %s %s %s-%s", items[e.Item], e.State, str(e.StartedOn), str(e.FinishedOn)))
	}
	for _, r := range doc.Reviews {
		x := ""
		if r.Experience != nil {
			x = exps[*r.Experience]
		}
		out = append(out, fmt.Sprintf("review %s %d %q exp=%s", items[r.Item], *r.Rating, str(r.Body), x))
	}
	for _, l := range doc.Lists {
		var li []string
		for _, it := range l.Items {
			li = append(li, fmt.Sprintf("%d:%s", it.Position, items[it.Item]))
		}
		out = append(out, fmt.Sprintf("list %s ranked=%v %v", l.Name, l.Ranked, li))
	}
	slices.Sort(out)
	return out
}
