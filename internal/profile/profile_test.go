package profile_test

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func exec(t *testing.T, s *testutil.Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func experience(t *testing.T, s *testutil.Server, userID, itemID int64, state db.ExperienceState, vis db.Visibility) db.Experience {
	t.Helper()
	x := testutil.CreateExperience(t, s.Pool, userID, itemID, state)
	exec(t, s, "update experiences set visibility = $1 where id = $2", vis, x.ID)
	return x
}

func entry(t *testing.T, s *testutil.Server, userID, itemID int64, backlog bool, vis db.Visibility) {
	t.Helper()
	exec(t, s, "insert into library_entries (user_id, media_item_id, backlog, visibility) values ($1, $2, $3, $4)", userID, itemID, backlog, vis)
}

func item(t *testing.T, s *testutil.Server, kind db.MediaKind, title string) db.MediaItem {
	t.Helper()
	return testutil.CreateItem(t, s.Pool, kind, title)
}

func get(t *testing.T, c *testutil.Client, path string, status int) string {
	t.Helper()
	res := c.Get(path)
	if res.StatusCode != status {
		t.Fatalf("GET %s: status %d, want %d\n%s", path, res.StatusCode, status, res.Body)
	}
	return res.Body
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q", w)
		}
	}
}

func lacks(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("body contains %q", u)
		}
	}
}

var counterRe = regexp.MustCompile(`id="count-([a-z-]+)">([\d,]+)<`)

func counters(body string) map[string]string {
	out := map[string]string{}
	for _, m := range counterRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func wantCounters(t *testing.T, body string, want map[string]string) {
	t.Helper()
	got := counters(body)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("counter %s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// section returns the HTML of the <section id=...> so assertions don't match other parts of the page.
func section(body, id string) string {
	start := strings.Index(body, `<section class="mb-8" id="`+id+`"`)
	if start < 0 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, "</section>"); end >= 0 {
		return rest[:end]
	}
	return rest
}

func TestRouting(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	testutil.CreateUser(t, s.Q, "carol")
	exec(t, s, "update users set disabled_at = now() where username = 'carol'")

	anon := s.Client(t)
	res := anon.Get("/@alice")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?next=/@alice" {
		t.Fatalf("anonymous: status %d location %q", res.StatusCode, res.Header.Get("Location"))
	}

	c := s.LoginAs(t, alice)
	for _, p := range []string{"/@alice", "/@ALICE", "/@alice/", "/@Alice?kind=book"} {
		contains(t, get(t, c, p, http.StatusOK), "<title>Alice · OpenShelf</title>")
	}
	for _, p := range []string{"/@nobody", "/@carol", "/@alice/extra", "/@", "/@a", "/alice", "/@alice?before=garbage"} {
		get(t, c, p, http.StatusNotFound)
	}
	if res := c.PostForm("/@alice", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("POST profile: status %d", res.StatusCode)
	}
}

func TestOwnProfileShowsEverything(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	game := item(t, s, db.MediaKindGame, "Private Game")
	book := item(t, s, db.MediaKindBook, "Private Book")
	back := item(t, s, db.MediaKindFilm, "Private Backlog")
	quit := item(t, s, db.MediaKindShow, "Private Quit")
	experience(t, s, alice.ID, game.ID, db.ExperienceStateActive, db.VisibilityPrivate)
	experience(t, s, alice.ID, book.ID, db.ExperienceStateFinished, db.VisibilityPrivate)
	experience(t, s, alice.ID, quit.ID, db.ExperienceStateAbandoned, db.VisibilityPrivate)
	entry(t, s, alice.ID, back.ID, true, db.VisibilityPrivate)
	testutil.CreateReview(t, s.Pool, alice.ID, book.ID, 80, db.VisibilityPrivate)
	testutil.CreateList(t, s.Pool, alice.ID, "Private List", db.VisibilityPrivate, book.ID)

	body := get(t, s.LoginAs(t, alice), "/@alice", http.StatusOK)
	contains(t, body, "Private Game", "Private Book", "Private Backlog", "Private Quit", "Private List",
		`id="visibility-note"`, `href="/settings"`, "@alice", "Member since")
	wantCounters(t, body, map[string]string{"completed": "1", "in-progress": "1", "backlog": "1", "abandoned": "1"})
}

// Bob's private rows must never reach Alice, in sections or in counts.
func TestOtherProfileHidesPrivateRows(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")

	for _, vis := range []db.Visibility{db.VisibilityPrivate, db.VisibilityInstance, db.VisibilityPublic} {
		v := string(vis)
		active := item(t, s, db.MediaKindGame, "Active "+v)
		x := experience(t, s, bob.ID, active.ID, db.ExperienceStateActive, vis)
		exec(t, s, "update experiences set notes = $1 where id = $2", "secret exp note "+v, x.ID)
		done := item(t, s, db.MediaKindBook, "Done "+v)
		experience(t, s, bob.ID, done.ID, db.ExperienceStateFinished, vis)
		quit := item(t, s, db.MediaKindFilm, "Quit "+v)
		experience(t, s, bob.ID, quit.ID, db.ExperienceStateAbandoned, vis)
		back := item(t, s, db.MediaKindShow, "Back "+v)
		entry(t, s, bob.ID, back.ID, true, vis)
		exec(t, s, "update library_entries set notes = $1 where media_item_id = $2", "secret entry note "+v, back.ID)
		rev := item(t, s, db.MediaKindBook, "Reviewed "+v)
		r := testutil.CreateReview(t, s.Pool, bob.ID, rev.ID, 60, vis)
		exec(t, s, "update reviews set body = $1 where id = $2", "Review body "+v, r.ID)
		testutil.CreateList(t, s.Pool, bob.ID, "List "+v, vis, rev.ID)
	}
	// A private review must not leak its rating onto a visible completion.
	shared := item(t, s, db.MediaKindBook, "Shared Done")
	experience(t, s, bob.ID, shared.ID, db.ExperienceStateFinished, db.VisibilityInstance)
	testutil.CreateReview(t, s.Pool, bob.ID, shared.ID, 100, db.VisibilityPrivate)

	body := get(t, s.LoginAs(t, alice), "/@bob", http.StatusOK)
	lacks(t, body, "private", "secret", `id="visibility-note"`)
	for _, v := range []string{"instance", "public"} {
		contains(t, body, "Active "+v, "Done "+v, "Quit "+v, "Back "+v, "Reviewed "+v, "Review body "+v, "List "+v)
	}
	wantCounters(t, body, map[string]string{"completed": "3", "in-progress": "2", "backlog": "2", "abandoned": "2"})
	if strings.Contains(section(body, "completed"), "out of 5 stars") {
		t.Error("completed card shows a rating from a private review")
	}
	for _, kind := range []string{"game", "book", "film", "show"} {
		lacks(t, get(t, s.LoginAs(t, alice), "/@bob?kind="+kind, http.StatusOK), "private", "secret")
	}

	own := get(t, s.LoginAs(t, bob), "/@bob", http.StatusOK)
	contains(t, own, "Active private", "Done private", "List private")
	lacks(t, own, "secret")
}

func TestKindTabsFilterEverySection(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	g := item(t, s, db.MediaKindGame, "Some Game")
	b := item(t, s, db.MediaKindBook, "Some Book")
	for _, it := range []db.MediaItem{g, b} {
		experience(t, s, alice.ID, it.ID, db.ExperienceStateFinished, db.VisibilityPrivate)
		testutil.CreateReview(t, s.Pool, alice.ID, it.ID, 50, db.VisibilityPrivate)
	}
	testutil.CreateList(t, s.Pool, alice.ID, "Game List", db.VisibilityPrivate, g.ID)
	c := s.LoginAs(t, alice)

	body := get(t, c, "/@alice?kind=book", http.StatusOK)
	contains(t, body, "Some Book", `href="/@alice?kind=game"`, `aria-current="page">Books<`)
	lacks(t, body, "Some Game", "Game List")
	wantCounters(t, body, map[string]string{"completed": "1"})

	body = get(t, c, "/@alice", http.StatusOK)
	contains(t, body, "Some Book", "Some Game", "Game List", `aria-current="page">All<`)
	wantCounters(t, body, map[string]string{"completed": "2"})
}

func TestInProgressShowsProgress(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	set := func(x db.Experience, unit string, cur float64, total any) {
		exec(t, s, "update experiences set progress_unit = $1, progress_current = $2, progress_total = $3 where id = $4", unit, cur, total, x.ID)
	}
	paused := experience(t, s, alice.ID, item(t, s, db.MediaKindShow, "Paused Show").ID, db.ExperienceStatePaused, db.VisibilityPrivate)
	set(paused, "episodes", 30, 62)
	book := experience(t, s, alice.ID, item(t, s, db.MediaKindBook, "Long Book").ID, db.ExperienceStateActive, db.VisibilityPrivate)
	set(book, "pages", 1079, 1496)
	game := experience(t, s, alice.ID, item(t, s, db.MediaKindGame, "Long Game").ID, db.ExperienceStateActive, db.VisibilityPrivate)
	set(game, "hours", 64, nil)

	sec := section(get(t, s.LoginAs(t, alice), "/@alice", http.StatusOK), "in-progress")
	contains(t, sec, "p. 1079 / 1496", "Ep 30 / 62", "64 h", "Paused")
	if strings.Index(sec, "Paused Show") < strings.Index(sec, "Long Book") || strings.Index(sec, "Paused Show") < strings.Index(sec, "Long Game") {
		t.Error("paused experiences should follow active ones")
	}
}

func TestCompletedGroupsByYear(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	exec(t, s, "update users set rating_display = 'ten' where id = $1", bob.ID)
	exec(t, s, "update users set timezone = 'America/New_York' where id = $1", alice.ID)

	finish := func(it db.MediaItem, on string) {
		x := experience(t, s, alice.ID, it.ID, db.ExperienceStateFinished, db.VisibilityPublic)
		exec(t, s, "update experiences set finished_on = $1::date where id = $2", on, x.ID)
	}
	twice := item(t, s, db.MediaKindBook, "Twice Read")
	finish(twice, "2024-03-01")
	finish(twice, "2024-09-01")
	yearly := item(t, s, db.MediaKindGame, "Yearly Game")
	finish(yearly, "2023-05-01")
	finish(yearly, "2024-02-01")
	testutil.CreateReview(t, s.Pool, alice.ID, twice.ID, 80, db.VisibilityPublic)
	// No finished_on: grouped by the owner's local date of the last update.
	late := item(t, s, db.MediaKindFilm, "New Year Film")
	x := experience(t, s, alice.ID, late.ID, db.ExperienceStateFinished, db.VisibilityPublic)
	exec(t, s, "update experiences set finished_on = null, updated_at = '2023-01-01 03:00:00Z' where id = $1", x.ID)

	sec := section(get(t, s.LoginAs(t, bob), "/@alice", http.StatusOK), "completed")
	y24, y23, y22 := strings.Index(sec, `data-year="2024"`), strings.Index(sec, `data-year="2023"`), strings.Index(sec, `data-year="2022"`)
	if y24 < 0 || y23 < y24 || y22 < y23 {
		t.Fatalf("years out of order: 2024@%d 2023@%d 2022@%d\n%s", y24, y23, y22, sec)
	}
	y2024, y2023, y2022 := sec[y24:y23], sec[y23:y22], sec[y22:]
	if strings.Count(sec, "Twice Read") != 1 || !strings.Contains(y2024, "Twice Read") || !strings.Contains(y2024, "×2") {
		t.Errorf("twice-read book should appear once in 2024 with ×2:\n%s", y2024)
	}
	if !strings.Contains(y2024, "Yearly Game") || !strings.Contains(y2023, "Yearly Game") || strings.Contains(y2023, "×") {
		t.Error("game finished in two years should appear once per year without a badge")
	}
	if !strings.Contains(y2022, "New Year Film") {
		t.Error("finish without a date should use the owner's timezone")
	}
	contains(t, y2024, "8/10")
}

func TestCompletedPaginates(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	for i := range 65 {
		x := experience(t, s, alice.ID, item(t, s, db.MediaKindFilm, fmt.Sprintf("Film %02d", i)).ID, db.ExperienceStateFinished, db.VisibilityPrivate)
		exec(t, s, "update experiences set finished_on = date '2024-01-01' + $1::int where id = $2", i, x.ID)
	}
	c := s.LoginAs(t, alice)
	body := get(t, c, "/@alice", http.StatusOK)
	sec := section(body, "completed")
	if n := strings.Count(sec, `data-section="completed"`); n != 60 {
		t.Fatalf("first page shows %d completions, want 60", n)
	}
	contains(t, sec, "Film 64", "Film 05", "All completions")
	lacks(t, sec, "Film 04")
	m := regexp.MustCompile(`hx-get="([^"]+)"`).FindStringSubmatch(sec)
	if m == nil {
		t.Fatal("no load-more link")
	}
	next := strings.ReplaceAll(m[1], "&amp;", "&")

	req, _ := http.NewRequest(http.MethodGet, s.URL+next, nil)
	req.Header.Set("HX-Request", "true")
	frag := c.Do(req)
	if frag.StatusCode != http.StatusOK || strings.Contains(frag.Body, "<html") {
		t.Fatalf("partial: status %d\n%s", frag.StatusCode, frag.Body)
	}
	if n := strings.Count(frag.Body, `data-section="completed"`); n != 5 {
		t.Fatalf("second page shows %d completions, want 5", n)
	}
	contains(t, frag.Body, "Film 04", "Film 00")
	lacks(t, frag.Body, "Film 05", "All completions", ">2024</h3>")

	full := section(get(t, c, next, http.StatusOK), "completed")
	contains(t, full, "Film 04")
	lacks(t, full, "Film 05")
}

func TestBacklogAndStalled(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	for i := range 30 {
		entry(t, s, alice.ID, item(t, s, db.MediaKindBook, fmt.Sprintf("Wanted %02d", i)).ID, true, db.VisibilityPrivate)
	}
	done := item(t, s, db.MediaKindBook, "Already Done")
	entry(t, s, alice.ID, done.ID, true, db.VisibilityPrivate)
	experience(t, s, alice.ID, done.ID, db.ExperienceStateFinished, db.VisibilityPrivate)
	going := item(t, s, db.MediaKindGame, "Going Now")
	entry(t, s, alice.ID, going.ID, true, db.VisibilityPrivate)
	experience(t, s, alice.ID, going.ID, db.ExperienceStatePaused, db.VisibilityPrivate)
	quit := item(t, s, db.MediaKindFilm, "Gave Up")
	experience(t, s, alice.ID, quit.ID, db.ExperienceStateAbandoned, db.VisibilityPrivate)

	body := get(t, s.LoginAs(t, alice), "/@alice", http.StatusOK)
	back := section(body, "backlog")
	if n := strings.Count(back, "/items/"); n != 24 {
		t.Errorf("backlog shows %d items, want 24", n)
	}
	contains(t, back, "Showing 24 of 30")
	lacks(t, back, "Already Done", "Going Now")
	wantCounters(t, body, map[string]string{"backlog": "30"})

	stalled := section(body, "stalled")
	contains(t, stalled, "Going Now", "Paused", "Gave Up", "Abandoned")
}

func TestReviewsAndLists(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	var last db.Review
	for i := range 7 {
		it := item(t, s, db.MediaKindBook, fmt.Sprintf("Reviewed %d", i))
		last = testutil.CreateReview(t, s.Pool, alice.ID, it.ID, 70, db.VisibilityPublic)
		exec(t, s, "update reviews set body = 'Spoiler text', contains_spoilers = true, updated_at = now() + $1 * interval '1 minute' where id = $2", i, last.ID)
	}
	l := testutil.CreateList(t, s.Pool, alice.ID, "Favorites", db.VisibilityPublic, last.MediaItemID)

	body := get(t, s.LoginAs(t, alice), "/@alice", http.StatusOK)
	revs := section(body, "reviews")
	if n := strings.Count(revs, `data-section="reviews"`); n != 5 {
		t.Errorf("shows %d reviews, want 5", n)
	}
	contains(t, revs, "Reviewed 6", `href="/reviews/`+last.PublicID.String()+`"`, "<details", "Contains spoilers")
	lacks(t, revs, "Reviewed 1<")
	contains(t, section(body, "lists"), "Favorites", `href="/lists/`+l.PublicID.String()+`"`, "1 item")
}

func TestEmptySections(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	testutil.CreateUser(t, s.Q, "bob")
	c := s.LoginAs(t, alice)

	own := get(t, c, "/@alice", http.StatusOK)
	contains(t, own, "Nothing in progress", "Nothing completed yet", "Backlog is empty", "No reviews yet", "No lists yet")

	other := get(t, c, "/@bob", http.StatusOK)
	contains(t, other, "Nothing shared yet")
	lacks(t, other, `id="in-progress"`, `id="completed"`, `id="backlog"`, `id="reviews"`, `id="lists"`, `id="stalled"`)
}

func TestProfileLinks(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	it := item(t, s, db.MediaKindBook, "Shared Book")
	r := testutil.CreateReview(t, s.Pool, bob.ID, it.ID, 80, db.VisibilityInstance)
	l := testutil.CreateList(t, s.Pool, bob.ID, "Bob's List", db.VisibilityInstance, it.ID)
	c := s.LoginAs(t, alice)

	contains(t, get(t, c, "/", http.StatusOK), `href="/@alice"`)
	contains(t, get(t, c, "/items/"+it.PublicID.String(), http.StatusOK), `href="/@bob"`)
	contains(t, get(t, c, "/reviews/"+r.PublicID.String(), http.StatusOK), `href="/@bob"`)
	contains(t, get(t, c, "/lists/"+l.PublicID.String(), http.StatusOK), `href="/@bob"`)
	contains(t, get(t, c, "/lists?tab=shared", http.StatusOK), `href="/@bob"`)
}

func TestQueryCountIsBounded(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	states := []db.ExperienceState{db.ExperienceStateActive, db.ExperienceStatePaused, db.ExperienceStateFinished, db.ExperienceStateAbandoned, ""}
	kinds := []db.MediaKind{db.MediaKindGame, db.MediaKindBook, db.MediaKindFilm, db.MediaKindShow}
	add := func(from, n int) {
		for i := from; i < from+n; i++ {
			it := item(t, s, kinds[i%4], fmt.Sprintf("Item %03d", i))
			entry(t, s, alice.ID, it.ID, i%5 == 4, db.VisibilityPublic)
			if st := states[i%5]; st != "" {
				experience(t, s, alice.ID, it.ID, st, db.VisibilityPublic)
			}
			if i%3 == 0 {
				testutil.CreateReview(t, s.Pool, alice.ID, it.ID, 60, db.VisibilityPublic)
			}
			if i%20 == 0 {
				testutil.CreateList(t, s.Pool, alice.ID, fmt.Sprintf("List %d", i), db.VisibilityPublic, it.ID)
			}
		}
	}
	add(0, 5)
	clients := []*testutil.Client{s.LoginAs(t, alice), s.LoginAs(t, bob)}
	measure := func() []int {
		var out []int
		for _, c := range clients {
			out = append(out, testutil.CountQueries(t, s.Pool, func() { get(t, c, "/@alice", http.StatusOK) }))
		}
		return out
	}
	small := measure()
	add(5, 300)
	large := measure()
	t.Logf("queries per profile page (owner, other): %v", large)
	for i := range small {
		if small[i] != large[i] || large[i] > 10 {
			t.Fatalf("profile ran %v queries with a small library and %v with a large one, want equal and at most 10", small, large)
		}
	}
}
