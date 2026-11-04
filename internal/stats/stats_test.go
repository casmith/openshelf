package stats_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/reviews"
	"github.com/casmith/openshelf/internal/stats"
	"github.com/casmith/openshelf/internal/testutil"
)

var ny = mustLoc("America/New_York")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func item(t *testing.T, pool *pgxpool.Pool, kind db.MediaKind, title string, genres ...string) int64 {
	t.Helper()
	it := testutil.CreateItem(t, pool, kind, title)
	exec(t, pool, `update media_items set genres = $2 where id = $1`, it.ID, append([]string{}, genres...))
	return it.ID
}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

type exp struct {
	state    db.ExperienceState
	finished string
	unit     string
	current  float64
}

func experience(t *testing.T, pool *pgxpool.Pool, userID, itemID int64, e exp) int64 {
	t.Helper()
	var fin *time.Time
	if e.finished != "" {
		d := day(e.finished)
		fin = &d
	}
	var unit *string
	var cur *float64
	if e.unit != "" {
		unit, cur = &e.unit, &e.current
	}
	var id int64
	if err := pool.QueryRow(context.Background(),
		`insert into experiences (user_id, media_item_id, state, finished_on, progress_unit, progress_current)
		 values ($1, $2, $3, $4, $5, $6) returning id`, userID, itemID, e.state, fin, unit, cur).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func event(t *testing.T, pool *pgxpool.Pool, expID int64, at time.Time, value float64) {
	t.Helper()
	exec(t, pool, `insert into progress_events (experience_id, occurred_at, value) values ($1, $2, $3)`, expID, at, value)
}

func review(t *testing.T, pool *pgxpool.Pool, userID, itemID int64, rating int, created time.Time) {
	t.Helper()
	r := testutil.CreateReview(t, pool, userID, itemID, rating, db.VisibilityPrivate)
	exec(t, pool, `update reviews set created_at = $2 where id = $1`, r.ID, created)
}

func user(t *testing.T, s *testutil.Server, name, tz string, display db.RatingDisplay) db.User {
	t.Helper()
	u := testutil.CreateUser(t, s.Q, name)
	exec(t, s.Pool, `update users set timezone = $2, rating_display = $3 where id = $1`, u.ID, tz, display)
	u, err := s.Q.GetUserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// fixture builds alice's hand-computed data set (America/New_York) plus noise from bob.
func fixture(t *testing.T, s *testutil.Server) (alice, bob db.User) {
	t.Helper()
	p := s.Pool
	alice = user(t, s, "alice", "America/New_York", db.RatingDisplayStars5)
	bob = user(t, s, "bob", "UTC", db.RatingDisplayTen)

	g1 := item(t, p, db.MediaKindGame, "G1", "rpg", "action")
	g2 := item(t, p, db.MediaKindGame, "G2", "rpg")
	b1 := item(t, p, db.MediaKindBook, "B1", "fantasy")
	b2 := item(t, p, db.MediaKindBook, "B2", "fantasy", "sci-fi")
	f1 := item(t, p, db.MediaKindFilm, "F1", "drama")
	s1 := item(t, p, db.MediaKindShow, "S1", "drama")
	f2 := item(t, p, db.MediaKindFilm, "F2", "horror")

	e := experience(t, p, alice.ID, g1, exp{state: db.ExperienceStateFinished, finished: "2026-01-10", unit: "hours", current: 25})
	event(t, p, e, time.Date(2026, 1, 5, 12, 0, 0, 0, ny), 10)
	event(t, p, e, time.Date(2026, 1, 9, 12, 0, 0, 0, ny), 25)
	experience(t, p, alice.ID, g2, exp{state: db.ExperienceStateFinished, finished: "2026-03-05"})
	experience(t, p, alice.ID, g2, exp{state: db.ExperienceStateFinished, finished: "2026-03-20"})
	experience(t, p, alice.ID, b1, exp{state: db.ExperienceStateFinished, finished: "2026-03-15", unit: "pages", current: 300})
	e = experience(t, p, alice.ID, b2, exp{state: db.ExperienceStateActive, unit: "pages", current: 200})
	event(t, p, e, time.Date(2025, 12, 31, 23, 0, 0, 0, ny), 50) // 2026 in UTC, 2025 locally
	event(t, p, e, time.Date(2026, 2, 1, 12, 0, 0, 0, ny), 120)
	event(t, p, e, time.Date(2026, 5, 1, 12, 0, 0, 0, ny), 200)
	experience(t, p, alice.ID, f1, exp{state: db.ExperienceStateFinished, finished: "2025-11-20"})
	experience(t, p, alice.ID, s1, exp{state: db.ExperienceStateFinished, finished: "2026-12-31", unit: "minutes", current: 120})
	experience(t, p, alice.ID, f2, exp{state: db.ExperienceStateAbandoned, finished: "2026-04-01"})

	review(t, p, alice.ID, g1, 90, time.Date(2026, 2, 1, 12, 0, 0, 0, ny))
	review(t, p, alice.ID, g2, 70, time.Date(2026, 4, 1, 12, 0, 0, 0, ny))
	review(t, p, alice.ID, b1, 75, time.Date(2026, 5, 1, 12, 0, 0, 0, ny))
	review(t, p, alice.ID, f1, 40, time.Date(2025, 11, 21, 12, 0, 0, 0, ny))

	e = experience(t, p, bob.ID, f2, exp{state: db.ExperienceStateFinished, finished: "2026-06-01", unit: "pages", current: 999})
	event(t, p, e, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 999)
	experience(t, p, bob.ID, g1, exp{state: db.ExperienceStateFinished, finished: "2026-02-02", unit: "hours", current: 500})
	review(t, p, bob.ID, f2, 10, time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))
	return alice, bob
}

var now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func load(t *testing.T, s *testutil.Server, u db.User, q url.Values) stats.Stats {
	t.Helper()
	st, err := stats.Load(context.Background(), s.Q, u, stats.ParsePeriod(q, now, u.Timezone))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func kindCounts(st stats.Stats) map[db.MediaKind]int {
	m := map[db.MediaKind]int{}
	for _, k := range st.ByKind {
		m[k.Kind] = k.Count
	}
	return m
}

func TestThisYearMatchesHandComputed(t *testing.T) {
	s := testutil.NewServer(t)
	alice, _ := fixture(t, s)
	st := load(t, s, alice, url.Values{})

	if st.Period.Year != 2026 || st.Completed != 4 {
		t.Fatalf("year %d completed %d, want 2026 and 4", st.Period.Year, st.Completed)
	}
	want := map[db.MediaKind]int{db.MediaKindGame: 2, db.MediaKindBook: 1, db.MediaKindFilm: 0, db.MediaKindShow: 1}
	if got := kindCounts(st); len(got) != 4 || got[db.MediaKindGame] != 2 || got[db.MediaKindBook] != 1 || got[db.MediaKindFilm] != 0 || got[db.MediaKindShow] != 1 {
		t.Errorf("by kind = %v, want %v", got, want)
	}

	if len(st.Timeline) != 12 {
		t.Fatalf("timeline has %d buckets, want 12", len(st.Timeline))
	}
	months := map[int]int{0: 1, 2: 2, 11: 1}
	for i, b := range st.Timeline {
		if b.Total != months[i] {
			t.Errorf("month %d total = %d, want %d", i+1, b.Total, months[i])
		}
	}
	if m := st.Timeline[2].ByKind; m[db.MediaKindGame] != 1 || m[db.MediaKindBook] != 1 {
		t.Errorf("march by kind = %v, want 1 game (re-finish counted once) and 1 book", m)
	}

	if st.Pages != 450 {
		t.Errorf("pages = %d, want 450 (300 finished without events + 200-50 logged in 2026)", st.Pages)
	}
	if st.Minutes != 25*60+120 || st.Hours() != 27 {
		t.Errorf("minutes = %d hours = %d, want 1620 and 27", st.Minutes, st.Hours())
	}

	if st.RatedCount != 3 || st.AvgRating == nil || *st.AvgRating != 78 {
		t.Errorf("rated %d avg %v, want 3 and 78", st.RatedCount, st.AvgRating)
	}
	dist := map[string]int{}
	for _, b := range st.Distribution {
		dist[b.Label] = b.Count
	}
	if len(st.Distribution) != 10 || dist["3.5★"] != 1 || dist["4★"] != 1 || dist["4.5★"] != 1 || dist["5★"] != 0 {
		t.Errorf("stars distribution = %v", st.Distribution)
	}

	var genres []string
	for _, g := range st.Genres {
		genres = append(genres, g.Name+":"+strconv.Itoa(g.Count))
	}
	if got := strings.Join(genres, ","); got != "rpg:2,action:1,drama:1,fantasy:1" {
		t.Errorf("genres = %s", got)
	}
}

func TestOtherPeriods(t *testing.T) {
	s := testutil.NewServer(t)
	alice, _ := fixture(t, s)

	last := load(t, s, alice, url.Values{"period": {"last-year"}})
	if last.Period.Year != 2025 || last.Completed != 1 || kindCounts(last)[db.MediaKindFilm] != 1 {
		t.Errorf("last year: year %d completed %d by kind %v", last.Period.Year, last.Completed, kindCounts(last))
	}
	if last.Pages != 50 || last.Minutes != 0 {
		t.Errorf("last year pages %d minutes %d, want 50 and 0", last.Pages, last.Minutes)
	}
	if last.RatedCount != 1 || *last.AvgRating != 40 {
		t.Errorf("last year rated %d avg %v", last.RatedCount, last.AvgRating)
	}

	custom := load(t, s, alice, url.Values{"year": {"2025"}})
	if custom.Period.Key != "year" || custom.Completed != 1 {
		t.Errorf("custom 2025: %+v", custom.Period)
	}

	all := load(t, s, alice, url.Values{"period": {"all"}})
	if all.Completed != 5 || all.Pages != 500 || all.RatedCount != 4 {
		t.Errorf("all time completed %d pages %d rated %d, want 5, 500, 4", all.Completed, all.Pages, all.RatedCount)
	}
	if len(all.Timeline) != 2 || all.Timeline[0].Label != "2025" || all.Timeline[0].Total != 1 || all.Timeline[1].Total != 4 {
		t.Errorf("all-time timeline = %+v", all.Timeline)
	}

	empty := load(t, s, alice, url.Values{"year": {"2010"}})
	if !empty.Empty() {
		t.Errorf("2010 should be empty: %+v", empty)
	}
}

func TestParsePeriodUsesViewerTimezone(t *testing.T) {
	newYearUTC := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	if p := stats.ParsePeriod(url.Values{}, newYearUTC, "America/New_York"); p.Year != 2025 {
		t.Errorf("New York this year = %d, want 2025", p.Year)
	}
	if p := stats.ParsePeriod(url.Values{}, newYearUTC, "UTC"); p.Year != 2026 {
		t.Errorf("UTC this year = %d, want 2026", p.Year)
	}
	if p := stats.ParsePeriod(url.Values{"year": {"nope"}, "period": {"bogus"}}, newYearUTC, "Not/AZone"); p.Key != "this-year" || p.Year != 2026 {
		t.Errorf("invalid input = %+v, want this-year 2026", p)
	}
}

func TestRatingScales(t *testing.T) {
	s := testutil.NewServer(t)
	u := user(t, s, "carol", "UTC", db.RatingDisplayTen)
	for i, r := range []int{90, 75, 70, 2} {
		it := item(t, s.Pool, db.MediaKindBook, "R"+strconv.Itoa(i))
		review(t, s.Pool, u.ID, it, r, now)
	}
	st := load(t, s, u, url.Values{})
	dist := map[string]int{}
	for _, b := range st.Distribution {
		dist[b.Label] = b.Count
	}
	if dist["9"] != 1 || dist["8"] != 1 || dist["7"] != 1 || dist["0"] != 1 || len(st.Distribution) != 11 {
		t.Errorf("ten distribution = %v", st.Distribution)
	}

	exec(t, s.Pool, `update users set rating_display = 'hundred' where id = $1`, u.ID)
	u.RatingDisplay = db.RatingDisplayHundred
	st = load(t, s, u, url.Values{})
	dist = map[string]int{}
	for _, b := range st.Distribution {
		dist[b.Label] = b.Count
	}
	if dist["90–100"] != 1 || dist["70–79"] != 2 || dist["0–9"] != 1 || len(st.Distribution) != 10 {
		t.Errorf("hundred distribution = %v", st.Distribution)
	}
}

func TestOneQueryPerWidget(t *testing.T) {
	s := testutil.NewServer(t)
	alice, _ := fixture(t, s)
	n := testutil.CountQueries(t, s.Pool, func() { load(t, s, alice, url.Values{}) })
	if n != 5 {
		t.Errorf("Load ran %d queries, want 5 (one per widget)", n)
	}

	empty := user(t, s, "dave", "UTC", db.RatingDisplayStars5)
	full, none := s.LoginAs(t, alice), s.LoginAs(t, empty)
	a := testutil.CountQueries(t, s.Pool, func() { full.Get("/stats") })
	b := testutil.CountQueries(t, s.Pool, func() { none.Get("/stats") })
	if a != b {
		t.Errorf("page with data ran %d queries, empty page %d; want equal", a, b)
	}
}

func TestPageRenders(t *testing.T) {
	s := testutil.NewServer(t)
	alice, _ := fixture(t, s)
	s.Deps.Now = func() time.Time { return now }
	res := s.LoginAs(t, alice).Get("/stats")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var avg strings.Builder
	_ = reviews.RatingDisplay(db.RatingDisplayStars5, ptr(int16(78))).Render(context.Background(), &avg)
	for _, want := range []string{
		"Statistics", "This year (2026)", `aria-current="page"`, `href="/stats"`,
		"Completed by media type", "Completions per month", "Rating distribution", "Top genres",
		"<svg", "<table", "Data table", "450", "27 h", avg.String(), "fill-[#2a78d6]", "rpg",
	} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(res.Body, " style=") {
		t.Error("page uses inline style attributes, which CSP forbids")
	}

	all := s.LoginAs(t, alice).Get("/stats?period=all")
	if !strings.Contains(all.Body, "All time") || !strings.Contains(all.Body, "Completions per year") {
		t.Error("all-time page missing period label or yearly chart")
	}
}

func TestEmptyState(t *testing.T) {
	s := testutil.NewServer(t)
	u := user(t, s, "erin", "UTC", db.RatingDisplayStars5)
	c := s.LoginAs(t, u)
	for _, path := range []string{"/stats", "/stats?period=all", "/stats?year=1999"} {
		res := c.Get(path)
		if res.StatusCode != 200 || !strings.Contains(res.Body, "No stats for this period") {
			t.Errorf("%s: status %d, empty state missing", path, res.StatusCode)
		}
	}

	only := user(t, s, "fay", "UTC", db.RatingDisplayStars5)
	it := item(t, s.Pool, db.MediaKindBook, "Rated only")
	review(t, s.Pool, only.ID, it, 80, now)
	res := s.LoginAs(t, only).Get("/stats?period=all")
	if res.StatusCode != 200 || !strings.Contains(res.Body, "Nothing completed in this period") {
		t.Errorf("ratings-only page: status %d, missing per-widget empty state", res.StatusCode)
	}
}

func TestRequiresLogin(t *testing.T) {
	s := testutil.NewServer(t)
	res := s.Client(t).Get("/stats")
	if res.StatusCode != 303 {
		t.Errorf("anonymous /stats status %d, want 303", res.StatusCode)
	}
}

func TestCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice, bob := fixture(t, s)

	st := load(t, s, bob, url.Values{"period": {"all"}})
	if st.Completed != 2 || st.Pages != 999 || st.Minutes != 500*60 || st.RatedCount != 1 || *st.AvgRating != 10 {
		t.Errorf("bob sees completed %d pages %d minutes %d rated %d; want only his own 2, 999, 30000, 1",
			st.Completed, st.Pages, st.Minutes, st.RatedCount)
	}
	for _, g := range st.Genres {
		if g.Name == "fantasy" || g.Name == "drama" {
			t.Errorf("bob sees alice's genre %q", g.Name)
		}
	}

	a := load(t, s, alice, url.Values{"period": {"all"}})
	if a.Pages != 500 || a.Minutes != 1620 || a.RatedCount != 4 {
		t.Errorf("bob's rows leaked into alice: pages %d minutes %d rated %d", a.Pages, a.Minutes, a.RatedCount)
	}

	res := s.LoginAs(t, bob).Get("/stats?period=all")
	if strings.Contains(res.Body, "fantasy") || !strings.Contains(res.Body, "999") {
		t.Error("bob's page shows alice's data or misses his own")
	}
}

func ptr[T any](v T) *T { return &v }
