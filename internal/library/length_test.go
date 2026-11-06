package library_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/library"
	"github.com/casmith/openshelf/internal/testutil"
)

// addLengthEntry adds an entry whose main estimate is minutes; minutes < 0 means no estimate row.
func addLengthEntry(t *testing.T, pool *pgxpool.Pool, userID int64, kind db.MediaKind, title string, minutes int) db.LibraryEntry {
	t.Helper()
	item := testutil.CreateItem(t, pool, kind, title)
	if minutes >= 0 {
		exec(t, pool, "insert into game_time_to_beat (media_item_id, normally_minutes) values ($1, $2)", item.ID, minutes)
	}
	return testutil.CreateEntry(t, pool, userID, item.ID)
}

func listKind(t *testing.T, q *db.Queries, u db.User, kind *db.MediaKind, query string) library.Page {
	t.Helper()
	v, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	p, err := library.List(context.Background(), q, u, library.ParseFilter(kind, v))
	if err != nil {
		t.Fatalf("list %q: %v", query, err)
	}
	return p
}

func lengthFixture(t *testing.T) (*pgxpool.Pool, *db.Queries, db.User) {
	pool := testutil.NewDB(t)
	q := db.New(pool)
	u := testutil.CreateUser(t, q, "alice")
	for _, g := range []struct {
		title   string
		kind    db.MediaKind
		minutes int
	}{
		{"Celeste", db.MediaKindGame, 600},
		{"Abzu", db.MediaKindGame, 600},
		{"Gris", db.MediaKindGame, 180},
		{"Elden Ring", db.MediaKindGame, 3600},
		{"Braid", db.MediaKindGame, -1},
		{"Dune", db.MediaKindBook, -1},
		{"Hollow Knight", db.MediaKindGame, 0},
		{"Fez", db.MediaKindGame, 600},
	} {
		addLengthEntry(t, pool, u.ID, g.kind, g.title, g.minutes)
	}
	return pool, q, u
}

func TestLengthSortNullsLastBothWays(t *testing.T) {
	_, q, u := lengthFixture(t)
	asc := []string{"Gris", "Abzu", "Celeste", "Fez", "Elden Ring", "Braid", "Dune", "Hollow Knight"}
	desc := []string{"Elden Ring", "Abzu", "Celeste", "Fez", "Gris", "Braid", "Dune", "Hollow Knight"}
	if got := list(t, q, u, "sort=length"); !slices.Equal(got, asc) {
		t.Errorf("asc: got %v, want %v", got, asc)
	}
	if got := list(t, q, u, "sort=length&dir=desc"); !slices.Equal(got, desc) {
		t.Errorf("desc: got %v, want %v", got, desc)
	}

	for _, query := range []string{"sort=length", "sort=length&dir=desc"} {
		all := list(t, q, u, query)
		for _, per := range []string{"1", "2", "3"} {
			var got []string
			var pages []library.Page
			v, _ := url.ParseQuery(query + "&per=" + per)
			for {
				p, err := library.List(context.Background(), q, u, library.ParseFilter(nil, v))
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, titles(p.Entries)...)
				pages = append(pages, p)
				if p.NextCursor == "" {
					break
				}
				v.Del("before")
				v.Set("after", p.NextCursor)
			}
			if !slices.Equal(got, all) {
				t.Errorf("%s per=%s forward: got %v, want %v", query, per, got, all)
			}
			var back []string
			for i := len(pages) - 1; i > 0; i-- {
				v.Del("after")
				v.Set("before", pages[i].PrevCursor)
				p, err := library.List(context.Background(), q, u, library.ParseFilter(nil, v))
				if err != nil {
					t.Fatal(err)
				}
				back = append(titles(p.Entries), back...)
			}
			if want := all[:len(all)-len(pages[len(pages)-1].Entries)]; !slices.Equal(back, want) {
				t.Errorf("%s per=%s backward: got %v, want %v", query, per, back, want)
			}
		}
	}
}

func TestLengthFilterBuckets(t *testing.T) {
	pool := testutil.NewDB(t)
	q := db.New(pool)
	u := testutil.CreateUser(t, q, "alice")
	for title, m := range map[string]int{"a299": 299, "b300": 300, "c899": 899, "d900": 900, "e2399": 2399, "f2400": 2400, "g_none": -1} {
		addLengthEntry(t, pool, u.ID, db.MediaKindGame, title, m)
	}
	addLengthEntry(t, pool, u.ID, db.MediaKindBook, "z_book", -1)
	for bucket, want := range map[string][]string{
		"under5": {"a299"},
		"5to15":  {"b300", "c899"},
		"15to40": {"d900", "e2399"},
		"40plus": {"f2400"},
		"none":   {"g_none"},
		"bogus":  {"a299", "b300", "c899", "d900", "e2399", "f2400", "g_none", "z_book"},
	} {
		if got := list(t, q, u, "length="+bucket); !slices.Equal(got, want) {
			t.Errorf("length=%s: got %v, want %v", bucket, got, want)
		}
	}
	exec(t, pool, `update library_entries le set backlog = true from media_items m
		where m.id = le.media_item_id and m.title in ('b300', 'c899', 'g_none', 'z_book')`)
	if got := list(t, q, u, "status=backlog&length=5to15&sort=length&dir=desc"); !slices.Equal(got, []string{"c899", "b300"}) {
		t.Errorf("backlog 5to15: got %v", got)
	}
	if got := list(t, q, u, "status=backlog&length=none"); !slices.Equal(got, []string{"g_none"}) {
		t.Errorf("backlog none: got %v", got)
	}
	if got := list(t, q, u, "status=backlog&sort=length"); !slices.Equal(got, []string{"b300", "c899", "g_none", "z_book"}) {
		t.Errorf("backlog by length: got %v", got)
	}
	book := db.MediaKindBook
	if got := titles(listKind(t, q, u, &book, "length=40plus&sort=length").Entries); !slices.Equal(got, []string{"z_book"}) {
		t.Errorf("book tab should ignore length: %v", got)
	}
}

func TestLengthDisplayAndControls(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	addLengthEntry(t, s.Pool, alice.ID, db.MediaKindGame, "Gris", 155)
	addLengthEntry(t, s.Pool, alice.ID, db.MediaKindGame, "Inside", 165)
	addLengthEntry(t, s.Pool, alice.ID, db.MediaKindGame, "Elden Ring", 3305)
	addLengthEntry(t, s.Pool, alice.ID, db.MediaKindGame, "Braid", -1)
	addLengthEntry(t, s.Pool, alice.ID, db.MediaKindBook, "Dune", -1)
	c := s.LoginAs(t, alice)

	grid := pageText(c.Get("/library/game?view=grid").Body)
	for _, want := range []string{"Gris ~2.5 h", "Inside ~3 h", "Elden Ring ~55 h"} {
		if !strings.Contains(grid, want) {
			t.Errorf("grid missing %q: %s", want, grid)
		}
	}
	if strings.Contains(grid, "Braid ~") {
		t.Error("unknown length should be omitted on cards")
	}

	table := c.Get("/library/game?view=table").Body
	if !strings.Contains(table, "<th>Length</th>") {
		t.Error("table should have a Length column")
	}
	cells := regexp.MustCompile(`data-length>([^<]*)<`).FindAllStringSubmatch(table, -1)
	var got []string
	for _, m := range cells {
		got = append(got, m[1])
	}
	if want := []string{"", "55 h", "2.5 h", "3 h"}; !slices.Equal(got, want) {
		t.Errorf("length cells: got %v, want %v", got, want)
	}

	hasSort := regexp.MustCompile(`<option value="length"[^>]*>Length</option>`)
	hasFilter := `name="length"`
	for path, want := range map[string]bool{"/library": true, "/library/game": true, "/library/book": false, "/library/film": false, "/library/show": false} {
		body := c.Get(path + "?view=grid").Body
		if hasSort.MatchString(body) != want || strings.Contains(body, hasFilter) != want {
			t.Errorf("%s: length sort/filter shown = %v/%v, want %v", path, hasSort.MatchString(body), strings.Contains(body, hasFilter), want)
		}
	}
	if body := c.Get("/library/book?sort=length&length=none").Body; strings.Contains(body, "Clear</a>") {
		t.Error("book tab should drop length state")
	}

	body := c.Get("/library/game?platform=pc").Body
	if !strings.Contains(body, `href="/library/game?platform=pc&amp;sort=length&amp;status=backlog"`) || !strings.Contains(body, "Quick wins") {
		t.Errorf("game tab should offer quick wins keeping filters: %s", body)
	}
	for _, path := range []string{"/library", "/library/book", "/library/game?status=backlog&sort=length"} {
		if strings.Contains(c.Get(path).Body, "data-quick-wins") {
			t.Errorf("%s should not show quick wins", path)
		}
	}
	text := pageText(c.Get("/library/game?sort=length").Body)
	if !strings.Contains(text, "Shortest first") || !strings.Contains(text, "Longest first") {
		t.Error("length sort should label directions")
	}

	page := c.Get("/library/game?sort=length&dir=desc&per=1").Body
	next := regexp.MustCompile(`href="(/library/game\?[^"]*after=[^"]*)"`).FindStringSubmatch(page)
	if next == nil {
		t.Fatal("missing next link")
	}
	u, _ := url.Parse(strings.ReplaceAll(next[1], "&amp;", "&"))
	if q := u.Query(); q.Get("sort") != "length" || q.Get("dir") != "desc" || q.Get("per") != "1" {
		t.Errorf("next link should keep state: %s", next[1])
	}
	if text := pageText(c.Get(u.String()).Body); !strings.Contains(text, "Inside") || strings.Contains(text, "Elden Ring ~") {
		t.Error("second page should continue longest first")
	}
}

func TestLengthGridOf500EntriesUsesFewQueries(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s.Pool, `insert into media_items (kind, title, sort_title)
		select 'game', 'Game ' || g, 'Game ' || lpad(g::text, 4, '0') from generate_series(1, 500) g`)
	exec(t, s.Pool, `insert into game_time_to_beat (media_item_id, normally_minutes)
		select id, (id % 50) * 60 from media_items where id % 4 <> 0`)
	exec(t, s.Pool, `insert into library_entries (user_id, media_item_id, backlog) select $1, id, true from media_items`, alice.ID)
	c := s.LoginAs(t, alice)
	for _, view := range []string{"grid", "table"} {
		c.Get("/library/game?view=" + view)
		var res *testutil.Response
		n := testutil.CountQueries(t, s.Pool, func() { res = c.Get("/library/game?status=backlog&sort=length&per=500") })
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
		if cards := strings.Count(res.Body, `href="/items/`) - strings.Count(res.Body, `href="/items/new"`); cards != 500 {
			t.Fatalf("%s: rendered %d entries, want 500", view, cards)
		}
		if n > 5 {
			t.Fatalf("%s: %d queries, want <= 5", view, n)
		}
	}
}
