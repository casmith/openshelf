package library_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/library"
	"github.com/casmith/openshelf/internal/seed"
	"github.com/casmith/openshelf/internal/testutil"
)

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// addExperience avoids testutil.CreateExperience, whose finished_on CASE fails to type-check.
func addExperience(t *testing.T, pool *pgxpool.Pool, userID, itemID int64, state db.ExperienceState) {
	t.Helper()
	exec(t, pool, "insert into experiences (user_id, media_item_id, state) values ($1, $2, $3)", userID, itemID, state)
}

func titles(rows []db.LibraryListEntriesRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Title
	}
	return out
}

func list(t *testing.T, q *db.Queries, u db.User, query string) []string {
	t.Helper()
	v, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	p, err := library.List(context.Background(), q, u, library.ParseFilter(nil, v))
	if err != nil {
		t.Fatalf("list %q: %v", query, err)
	}
	return titles(p.Entries)
}

func TestAddRejectsDuplicateButAllowsEditions(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Hades")
	pc := testutil.CreateEdition(t, s.Pool, item.ID, "pc")
	sw := testutil.CreateEdition(t, s.Pool, item.ID, "switch")
	path := "/items/" + item.PublicID.String() + "/library"

	post := func(edition string) *testutil.Response {
		return c.PostForm(path, url.Values{"edition": {edition}, "ownership": {"owned"}, "visibility": {"private"}})
	}
	if res := post(""); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("first add: %d\n%s", res.StatusCode, res.Body)
	}
	res := post("")
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "already in your library") {
		t.Fatalf("duplicate add: want 422, got %d", res.StatusCode)
	}
	for _, e := range []string{pc.PublicID.String(), sw.PublicID.String()} {
		if res := post(e); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("add edition: %d\n%s", res.StatusCode, res.Body)
		}
	}
	if res := post(pc.PublicID.String()); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate edition add: want 422, got %d", res.StatusCode)
	}
	if n := count(t, s.Pool, "select count(*) from library_entries where user_id = $1", alice.ID); n != 3 {
		t.Fatalf("entries = %d, want 3", n)
	}
	if n := count(t, s.Pool, "select count(*) from activity_events where user_id = $1 and verb = 'added' and library_entry_id is not null", alice.ID); n != 3 {
		t.Fatalf("added events = %d, want 3", n)
	}

	other := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Celeste")
	otherEd := testutil.CreateEdition(t, s.Pool, other.ID, "pc")
	if res := post(otherEd.PublicID.String()); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("edition of another item: want 422, got %d", res.StatusCode)
	}

	page := c.Get("/items/" + item.PublicID.String())
	if page.StatusCode != http.StatusOK || strings.Count(page.Body, "data-entry=") != 3 {
		t.Fatalf("item page should list 3 entries: %d", page.StatusCode)
	}
}

func TestAddRecordsActivityInTransaction(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	ctx := context.Background()
	entry, err := library.Add(ctx, s.Deps, alice, item, nil, library.EntryInput{Visibility: db.VisibilityInstance, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s.Pool, "select count(*) from activity_events where library_entry_id = $1 and verb = 'added' and visibility = 'instance'", entry.ID); n != 1 {
		t.Fatalf("added events = %d", n)
	}
	if _, err := library.Add(ctx, s.Deps, alice, item, nil, library.EntryInput{Visibility: db.VisibilityPrivate, Tags: []string{}}); !errors.Is(err, library.ErrDuplicate) {
		t.Fatalf("duplicate: err = %v", err)
	}
	if n := count(t, s.Pool, "select count(*) from activity_events where user_id = $1", alice.ID); n != 1 {
		t.Fatalf("failed add must not record activity; events = %d", n)
	}
}

func TestStatusFiltersMatchDerivedDefinitions(t *testing.T) {
	pool := testutil.NewDB(t)
	q := db.New(pool)
	u := testutil.CreateUser(t, q, "alice")

	type setup struct {
		backlog      bool
		played       string // user override: "", "true", "false"
		playedSource bool
		experiences  []db.ExperienceState
	}
	cases := map[string]setup{
		"Backlogged":      {backlog: true},
		"Active":          {backlog: true, experiences: []db.ExperienceState{db.ExperienceStateActive}},
		"Paused":          {experiences: []db.ExperienceState{db.ExperienceStatePaused}},
		"Finished":        {experiences: []db.ExperienceState{db.ExperienceStateFinished}},
		"FinishedBacklog": {backlog: true, experiences: []db.ExperienceState{db.ExperienceStateFinished}},
		"Abandoned":       {backlog: true, experiences: []db.ExperienceState{db.ExperienceStateAbandoned}},
		"NotPlayedFinish": {played: "false", experiences: []db.ExperienceState{db.ExperienceStateFinished}},
		"SourcePlayed":    {playedSource: true},
		"UserPlayed":      {played: "true"},
		"Untouched":       {},
	}
	for title, c := range cases {
		item := testutil.CreateItem(t, pool, db.MediaKindGame, title)
		e := testutil.CreateEntry(t, pool, u.ID, item.ID)
		exec(t, pool, `update library_entries set backlog = $2, played_source = case when $3 then true end, played_user = nullif($4, '')::boolean where id = $1`,
			e.ID, c.backlog, c.playedSource, c.played)
		for _, st := range c.experiences {
			addExperience(t, pool, u.ID, item.ID, st)
		}
	}
	// Another user's experiences must not affect alice's derived status.
	bob := testutil.CreateUser(t, q, "bob")
	var untouchedID int64
	if err := pool.QueryRow(context.Background(), "select id from media_items where title = 'Untouched'").Scan(&untouchedID); err != nil {
		t.Fatal(err)
	}
	addExperience(t, pool, bob.ID, untouchedID, db.ExperienceStateActive)

	want := map[string][]string{
		"in_progress": {"Active", "Paused"},
		"finished":    {"Finished", "FinishedBacklog", "NotPlayedFinish"},
		"backlog":     {"Abandoned", "Backlogged"},
		"played":      {"Active", "Finished", "FinishedBacklog", "SourcePlayed", "UserPlayed"},
		"not_played":  {"Abandoned", "Backlogged", "NotPlayedFinish", "Paused", "Untouched"},
	}
	for status, w := range want {
		got := list(t, q, u, "status="+status)
		slices.Sort(got)
		if !slices.Equal(got, w) {
			t.Errorf("status=%s: got %v, want %v", status, got, w)
		}
	}
}

func TestFilters(t *testing.T) {
	pool := testutil.NewDB(t)
	q := db.New(pool)
	u := testutil.CreateUser(t, q, "alice")

	halo := testutil.CreateItem(t, pool, db.MediaKindGame, "Halo")
	haloXbox := testutil.CreateEdition(t, pool, halo.ID, "xbox")
	exec(t, pool, "update editions set format = 'physical' where id = $1", haloXbox.ID)
	e := testutil.CreateEntry(t, pool, u.ID, halo.ID)
	exec(t, pool, "update library_entries set edition_id = $2, ownership_user = 'owned', tags = '{couch}', created_at = '2026-01-10' where id = $1", e.ID, haloXbox.ID)
	exec(t, pool, "update media_items set genres = '{shooter}' where id = $1", halo.ID)
	testutil.CreateReview(t, pool, u.ID, halo.ID, 90, db.VisibilityPrivate)

	dune := testutil.CreateItem(t, pool, db.MediaKindBook, "Dune")
	e = testutil.CreateEntry(t, pool, u.ID, dune.ID)
	exec(t, pool, "update library_entries set ownership_source = 'borrowed', created_at = '2026-03-01' where id = $1", e.ID)
	testutil.CreateReview(t, pool, u.ID, dune.ID, 50, db.VisibilityPrivate)

	arrival := testutil.CreateItem(t, pool, db.MediaKindFilm, "Arrival")
	testutil.CreateEntry(t, pool, u.ID, arrival.ID)

	for query, w := range map[string][]string{
		"ownership=owned":          {"Halo"},
		"ownership=borrowed":       {"Dune"},
		"ownership=not_owned":      {"Arrival"},
		"platform=xbox":            {"Halo"},
		"format=physical":          {"Halo"},
		"genre=shooter":            {"Halo"},
		"tag=couch":                {"Halo"},
		"rating=80":                {"Halo"},
		"rating=rated":             {"Dune", "Halo"},
		"rating=unrated":           {"Arrival"},
		"added_from=2026-02-01":    {"Arrival", "Dune"},
		"added_to=2026-02-01":      {"Halo"},
		"sort=rating":              {"Halo", "Dune", "Arrival"},
		"sort=rating&dir=asc":      {"Dune", "Halo", "Arrival"},
		"sort=added&dir=asc":       {"Halo", "Dune", "Arrival"},
		"ownership=bogus&sort=xyz": {"Arrival", "Dune", "Halo"},
	} {
		got := list(t, q, u, query)
		if query == "rating=rated" {
			slices.Sort(got)
		}
		if !slices.Equal(got, w) {
			t.Errorf("%s: got %v, want %v", query, got, w)
		}
	}

	book := db.MediaKindBook
	p, err := library.List(context.Background(), q, u, library.ParseFilter(&book, nil))
	if err != nil || !slices.Equal(titles(p.Entries), []string{"Dune"}) {
		t.Fatalf("kind=book: %v %v", titles(p.Entries), err)
	}
}

func TestKeysetPaginationAllSorts(t *testing.T) {
	pool := testutil.NewDB(t)
	q := db.New(pool)
	u := testutil.CreateUser(t, q, "alice")
	names := []string{"Echo", "alpha", "Delta", "bravo", "Charlie", "Foxtrot", "Golf"}
	for i, n := range names {
		item := testutil.CreateItem(t, pool, db.MediaKindBook, n)
		e := testutil.CreateEntry(t, pool, u.ID, item.ID)
		// Duplicate sort keys exercise the public_id tiebreak.
		exec(t, pool, "update library_entries set created_at = now() - make_interval(days => $2) where id = $1", e.ID, i/2)
		if i%3 != 0 {
			exec(t, pool, "update media_items set release_year = $2 where id = $1", item.ID, 2000+i%2)
			testutil.CreateReview(t, pool, u.ID, item.ID, 20*(i%3), db.VisibilityPrivate)
		}
	}
	for _, query := range []string{"", "dir=desc", "sort=added", "sort=added&dir=asc", "sort=release", "sort=release&dir=asc", "sort=rating", "sort=rating&dir=asc"} {
		all := list(t, q, u, query+"&per=100")
		if len(all) != len(names) {
			t.Fatalf("%s: got %d entries", query, len(all))
		}
		var got []string
		var cursors []string
		v, _ := url.ParseQuery(query + "&per=3")
		for {
			p, err := library.List(context.Background(), q, u, library.ParseFilter(nil, v))
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, titles(p.Entries)...)
			cursors = append(cursors, p.PrevCursor)
			if p.NextCursor == "" {
				break
			}
			v.Del("before")
			v.Set("after", p.NextCursor)
		}
		if !slices.Equal(got, all) {
			t.Errorf("%s forward: got %v, want %v", query, got, all)
		}
		// Walk back from the last page using prev cursors.
		last := cursors[len(cursors)-1]
		v.Del("after")
		v.Set("before", last)
		p, err := library.List(context.Background(), q, u, library.ParseFilter(nil, v))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(titles(p.Entries), all[3:6]) {
			t.Errorf("%s back: got %v, want %v", query, titles(p.Entries), all[3:6])
		}
		if p.NextCursor == "" || p.PrevCursor == "" {
			t.Errorf("%s back: middle page should have both cursors", query)
		}
	}
	if got := list(t, q, u, ""); !slices.Equal(got[:3], []string{"alpha", "bravo", "Charlie"}) {
		t.Errorf("title sort should be case-insensitive: %v", got)
	}
}

func TestGridOf500EntriesUsesFewQueries(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s.Pool, `insert into media_items (kind, title, sort_title, genres)
		select (array['game','book','film','show']::media_kind[])[1 + g % 4], 'Item ' || g, 'Item ' || lpad(g::text, 4, '0'), '{drama}'
		from generate_series(1, 500) g`)
	exec(t, s.Pool, `insert into library_entries (user_id, media_item_id, tags) select $1, id, '{fav}' from media_items`, alice.ID)
	exec(t, s.Pool, `insert into experiences (user_id, media_item_id, state) select $1, id, 'finished' from media_items where id % 3 = 0`, alice.ID)
	exec(t, s.Pool, `insert into reviews (user_id, media_item_id, rating) select $1, id, 80 from media_items where id % 2 = 0`, alice.ID)
	c := s.LoginAs(t, alice)

	for _, view := range []string{"grid", "table"} {
		// The view toggle writes the session once; measure the steady state after it.
		c.Get("/library?view=" + view)
		var res *testutil.Response
		n := testutil.CountQueries(t, s.Pool, func() { res = c.Get("/library?per=500") })
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
		if cards := strings.Count(res.Body, `href="/items/`); cards != 500 {
			t.Fatalf("%s: rendered %d entries, want 500", view, cards)
		}
		t.Logf("%s: %d queries", view, n)
		if n > 5 {
			t.Fatalf("%s: %d queries, want <= 5", view, n)
		}
	}
}

func TestViewToggleRememberedInSession(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")
	testutil.CreateEntry(t, s.Pool, alice.ID, item.ID)
	c := s.LoginAs(t, alice)
	if res := c.Get("/library"); !strings.Contains(res.Body, `<ul class="grid`) || strings.Contains(res.Body, "<table") {
		t.Fatal("default view should be grid")
	}
	c.Get("/library?view=table")
	if res := c.Get("/library/film"); !strings.Contains(res.Body, "<table") {
		t.Fatal("table view should be remembered")
	}
}

func TestKindPagesAndPartial(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	for kind, title := range map[db.MediaKind]string{db.MediaKindGame: "Hades", db.MediaKindBook: "Dune"} {
		testutil.CreateEntry(t, s.Pool, alice.ID, testutil.CreateItem(t, s.Pool, kind, title).ID)
	}
	c := s.LoginAs(t, alice)
	res := c.Get("/library/game")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "Hades") || strings.Contains(res.Body, "Dune") {
		t.Fatalf("game page: %d", res.StatusCode)
	}
	if !regexp.MustCompile(`href="/library/game"[^>]*aria-current="page"`).MatchString(res.Body) {
		t.Error("sidebar should mark Games active")
	}
	if res := c.Get("/library/nope"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", res.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/library?status=backlog", nil)
	req.Header.Set("HX-Request", "true")
	res = c.Do(req)
	if res.StatusCode != http.StatusOK || strings.Contains(res.Body, "<html") || !strings.Contains(res.Body, `id="library"`) {
		t.Fatalf("partial should render only the library body: %d", res.StatusCode)
	}
}

func TestUpdatePlayedAndRemove(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Portal 2")
	e := testutil.CreateEntry(t, s.Pool, alice.ID, item.ID)
	exec(t, s.Pool, "update library_entries set played_source = true where id = $1", e.ID)
	c := s.LoginAs(t, alice)
	base := "/library/" + e.PublicID.String()

	if res := c.Get(base + "/edit"); res.StatusCode != http.StatusOK {
		t.Fatalf("edit page: %d", res.StatusCode)
	}
	res := c.PostForm(base, url.Values{"ownership": {"borrowed"}, "backlog": {"on"}, "acquired_on": {"2026-05-01"},
		"tags": {"Co-op, puzzle, co-op"}, "notes": {"with Sam"}, "visibility": {"public"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/items/"+item.PublicID.String() {
		t.Fatalf("update: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var got db.LibraryEntry
	if err := s.Pool.QueryRow(context.Background(), "select ownership_user, backlog, tags, notes, visibility from library_entries where id = $1", e.ID).
		Scan(&got.OwnershipUser, &got.Backlog, &got.Tags, &got.Notes, &got.Visibility); err != nil {
		t.Fatal(err)
	}
	if *got.OwnershipUser != db.OwnershipBorrowed || !got.Backlog || !slices.Equal(got.Tags, []string{"co-op", "puzzle"}) || *got.Notes != "with Sam" || got.Visibility != db.VisibilityPublic {
		t.Fatalf("update not applied: %+v", got)
	}
	if res := c.PostForm(base, url.Values{"visibility": {"everyone"}, "acquired_on": {"soon"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid update: %d", res.StatusCode)
	}

	played := func() (user *bool, source *bool) {
		if err := s.Pool.QueryRow(context.Background(), "select played_user, played_source from library_entries where id = $1", e.ID).Scan(&user, &source); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, step := range []struct {
		value string
		want  *bool
	}{{"false", new(false)}, {"true", new(true)}, {"reset", nil}} {
		if res := c.PostForm(base+"/played", url.Values{"played": {step.value}}); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("played=%s: %d", step.value, res.StatusCode)
		}
		user, source := played()
		if (user == nil) != (step.want == nil) || (user != nil && *user != *step.want) {
			t.Errorf("played=%s: played_user = %v", step.value, user)
		}
		if source == nil || !*source {
			t.Errorf("played=%s must not touch played_source", step.value)
		}
	}
	if res := c.PostForm(base+"/played", url.Values{"played": {"maybe"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad played value: %d", res.StatusCode)
	}

	if res := c.Form(http.MethodDelete, base, nil, true); res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") == "" {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if n := count(t, s.Pool, "select count(*) from library_entries where id = $1", e.ID); n != 0 {
		t.Fatal("entry not deleted")
	}
}

func TestCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Secret Game")
	e := testutil.CreateEntry(t, s.Pool, alice.ID, item.ID)
	exec(t, s.Pool, "update library_entries set tags = '{alicetag}', notes = 'alice notes', ownership_user = 'owned', visibility = 'public' where id = $1", e.ID)
	c := s.LoginAs(t, bob)

	res := c.Get("/library")
	if strings.Contains(res.Body, "Secret Game") || strings.Contains(res.Body, "alicetag") {
		t.Fatal("bob's library shows alice's entry or tags")
	}
	res = c.Get("/items/" + item.PublicID.String())
	if res.StatusCode != http.StatusOK || strings.Contains(res.Body, e.PublicID.String()) || strings.Contains(res.Body, "alice notes") {
		t.Fatal("item page shows alice's entry to bob")
	}
	base := "/library/" + e.PublicID.String()
	for name, r := range map[string]*testutil.Response{
		"edit":   c.Get(base + "/edit"),
		"update": c.PostForm(base, url.Values{"visibility": {"private"}, "ownership": {"not_owned"}}),
		"played": c.PostForm(base+"/played", url.Values{"played": {"false"}}),
		"delete": c.Form(http.MethodDelete, base, nil, false),
		"remove": c.PostForm(base+"/delete", nil),
	} {
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: bob got %d, want 404", name, r.StatusCode)
		}
	}
	var own db.Ownership
	var playedUser *bool
	if err := s.Pool.QueryRow(context.Background(), "select ownership_user, played_user from library_entries where id = $1", e.ID).Scan(&own, &playedUser); err != nil {
		t.Fatalf("alice's entry should still exist: %v", err)
	}
	if own != db.OwnershipOwned || playedUser != nil {
		t.Fatal("bob modified alice's entry")
	}

	// Bob can add the same item to his own library independently.
	if res := c.PostForm("/items/"+item.PublicID.String()+"/library", url.Values{"ownership": {"owned"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("bob add: %d", res.StatusCode)
	}
	if got := list(t, s.Q, bob, ""); !slices.Equal(got, []string{"Secret Game"}) {
		t.Fatalf("bob library: %v", got)
	}
}

func TestAnonymousRedirected(t *testing.T) {
	s := testutil.NewServer(t)
	if res := s.Client(t).Get("/library"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", res.StatusCode)
	}
}

func TestNormalizeTags(t *testing.T) {
	got := library.NormalizeTags(" Co-op ,  Local   Multi, ,co-op")
	if !slices.Equal(got, []string{"co-op", "local multi"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSeedDataRenders(t *testing.T) {
	s := testutil.NewServer(t)
	if _, err := seed.Run(context.Background(), s.Pool, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := s.Client(t)
	if res := c.PostForm("/login", url.Values{"username": {"alice"}, "password": {seed.Password}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", res.StatusCode)
	}
	for _, p := range []string{"/library", "/library/game", "/library/book", "/library/film", "/library/show", "/library?view=table&sort=rating", "/library?status=backlog"} {
		if res := c.Get(p); res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", p, res.StatusCode)
		}
	}
	res := c.Get("/library?status=in_progress&view=grid")
	if !strings.Contains(res.Body, "Valheim") || strings.Contains(res.Body, "Jet Set Radio") {
		t.Fatal("seeded in-progress filter is wrong")
	}
	var id string
	if err := s.Pool.QueryRow(context.Background(), "select public_id::text from media_items where title = 'Valheim'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if res := c.Get("/items/" + id); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "data-entry=") {
		t.Fatalf("item page: %d", res.StatusCode)
	}
}
