package journal_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/journal"
	"github.com/casmith/openshelf/internal/testutil"
)

func record(t *testing.T, s *testutil.Server, ev activity.Event) db.ActivityEvent {
	t.Helper()
	e, err := activity.Record(context.Background(), s.Q, ev)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// testutil.CreateExperience fails on an untyped $3, so tests insert experiences directly.
func createExperience(t *testing.T, s *testutil.Server, userID, itemID int64, state db.ExperienceState) db.Experience {
	t.Helper()
	var e db.Experience
	err := s.Pool.QueryRow(context.Background(),
		`insert into experiences (user_id, media_item_id, state, finished_on)
		 values ($1, $2, $3::experience_state, case when $3::experience_state in ('finished', 'abandoned') then current_date end)
		 returning id`, userID, itemID, state).Scan(&e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func setTimezone(t *testing.T, s *testutil.Server, u db.User, tz string) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), "update users set timezone = $1 where id = $2", tz, u.ID); err != nil {
		t.Fatal(err)
	}
}

var dayHeader = regexp.MustCompile(`<time datetime="(\d{4}-\d{2}-\d{2})">`)

func dayHeaders(body string) []string {
	var out []string
	for _, m := range dayHeader.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestGroupByDayAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	ts := []string{
		"2026-11-02T04:30:00Z", // Nov 1 23:30 EST
		"2026-11-01T04:30:00Z", // Nov 1 00:30 EDT
		"2026-03-09T04:30:00Z", // Mar 9 00:30 EDT
		"2026-03-09T03:30:00Z", // Mar 8 23:30 EDT
		"2026-03-08T05:30:00Z", // Mar 8 00:30 EST
		"2026-03-08T04:30:00Z", // Mar 7 23:30 EST
	}
	var events []journal.Event
	for _, s := range ts {
		at, _ := time.Parse(time.RFC3339, s)
		events = append(events, journal.Event{OccurredAt: at})
	}
	days := journal.GroupByDay(events, loc)
	var got []string
	for _, d := range days {
		got = append(got, d.Date.Format("2006-01-02")+":"+string(rune('0'+len(d.Events))))
	}
	want := "2026-11-01:2 2026-03-09:1 2026-03-08:2 2026-03-07:1"
	if strings.Join(got, " ") != want {
		t.Fatalf("days = %v, want %s", got, want)
	}
}

func TestJournalGroupsDaysInUserTimezone(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	setTimezone(t, s, alice, "America/New_York")
	s.Deps.Now = func() time.Time { return time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC) }
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Piranesi")
	for _, ts := range []string{"2026-03-08T04:30:00Z", "2026-03-08T05:30:00Z", "2026-03-09T03:30:00Z", "2026-03-09T04:30:00Z", "2026-11-01T04:30:00Z", "2026-11-02T04:30:00Z"} {
		at, _ := time.Parse(time.RFC3339, ts)
		record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbAdded, MediaItemID: item.ID, OccurredAt: at})
	}
	res := s.LoginAs(t, alice).Get("/journal")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	got := strings.Join(dayHeaders(res.Body), " ")
	if want := "2026-11-01 2026-03-09 2026-03-08 2026-03-07"; got != want {
		t.Fatalf("day headers = %q, want %q", got, want)
	}
	if !strings.Contains(res.Body, "Sunday, November 1, 2026") {
		t.Error("missing full day label")
	}
}

func TestJournalRendersEveryVerb(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Outer Wilds")
	entry := testutil.CreateEntry(t, s.Pool, alice.ID, item.ID)
	exp := createExperience(t, s, alice.ID, item.ID, db.ExperienceStateActive)
	if _, err := s.Pool.Exec(context.Background(), "update experiences set progress_unit = 'hours', progress_current = 12 where id = $1", exp.ID); err != nil {
		t.Fatal(err)
	}
	rev := testutil.CreateReview(t, s.Pool, alice.ID, item.ID, 90, db.VisibilityPrivate)
	list := testutil.CreateList(t, s.Pool, alice.ID, "Space games", db.VisibilityPrivate, item.ID)

	want := map[db.ActivityVerb]string{
		db.ActivityVerbAdded:      "to your library",
		db.ActivityVerbStarted:    "Started playing",
		db.ActivityVerbPaused:     "Paused",
		db.ActivityVerbResumed:    "Resumed",
		db.ActivityVerbProgressed: "12 hours",
		db.ActivityVerbFinished:   "Finished",
		db.ActivityVerbAbandoned:  "Stopped playing",
		db.ActivityVerbRated:      "Rated",
		db.ActivityVerbReviewed:   "Reviewed",
		db.ActivityVerbListed:     "Space games",
	}
	for _, v := range db.AllActivityVerbValues() {
		ev := activity.Event{UserID: alice.ID, Verb: v, MediaItemID: item.ID}
		switch v {
		case db.ActivityVerbAdded:
			ev.LibraryEntryID = &entry.ID
		case db.ActivityVerbRated, db.ActivityVerbReviewed:
			ev.ReviewID = &rev.ID
		case db.ActivityVerbListed:
			ev.ListID = &list.ID
		default:
			ev.ExperienceID = &exp.ID
		}
		record(t, s, ev)
		if _, ok := want[v]; !ok {
			t.Fatalf("no expectation for verb %s", v)
		}
	}

	c := s.LoginAs(t, alice)
	res := c.Get("/journal")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for v, text := range want {
		if !strings.Contains(res.Body, `data-verb="`+string(v)+`"`) {
			t.Errorf("verb %s not rendered", v)
		}
		if !strings.Contains(html.UnescapeString(res.Body), text) {
			t.Errorf("verb %s: missing %q", v, text)
		}
	}
}

func TestJournalSurvivesDeletedSources(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")
	exp := createExperience(t, s, alice.ID, item.ID, db.ExperienceStateFinished)
	rev := testutil.CreateReview(t, s.Pool, alice.ID, item.ID, 80, db.VisibilityPrivate)
	list := testutil.CreateList(t, s.Pool, alice.ID, "Gone", db.VisibilityPrivate, item.ID)
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbFinished, MediaItemID: item.ID, ExperienceID: &exp.ID})
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbRated, MediaItemID: item.ID, ReviewID: &rev.ID})
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbListed, MediaItemID: item.ID, ListID: &list.ID})
	// Events whose source references are null still have to render.
	for _, v := range db.AllActivityVerbValues() {
		record(t, s, activity.Event{UserID: alice.ID, Verb: v, MediaItemID: item.ID})
	}
	ctx := context.Background()
	for _, q := range []string{"delete from reviews", "delete from lists", "delete from experiences"} {
		if _, err := s.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	c := s.LoginAs(t, alice)
	for _, p := range []string{"/journal", "/"} {
		if res := c.Get(p); res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
	}
}

func TestJournalFilters(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Rez")
	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Anathem")
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbAdded, MediaItemID: game.ID})
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbFinished, MediaItemID: game.ID})
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbAdded, MediaItemID: book.ID})
	c := s.LoginAs(t, alice)

	count := func(path string) int {
		t.Helper()
		res := c.Get(path)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, res.StatusCode)
		}
		return strings.Count(res.Body, "data-verb=")
	}
	if n := count("/journal"); n != 3 {
		t.Errorf("unfiltered: %d events", n)
	}
	if n := count("/journal?kind=game"); n != 2 {
		t.Errorf("kind=game: %d events", n)
	}
	if n := count("/journal?verb=added"); n != 2 {
		t.Errorf("verb=added: %d events", n)
	}
	if n := count("/journal?kind=book&verb=finished"); n != 0 {
		t.Errorf("book+finished: %d events", n)
	}
	if n := count("/journal?kind=bogus"); n != 3 {
		t.Errorf("invalid kind should be ignored: %d events", n)
	}
	if res := c.Get("/journal?before=nope"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor: status %d", res.StatusCode)
	}
}

var (
	nextLink = regexp.MustCompile(`href="(/journal\?[^"]*before=[^"]*)"`)
	prevLink = regexp.MustCompile(`href="(/journal\?[^"]*after=[^"]*)"`)
	titleRe  = regexp.MustCompile(`>(Item \d{3})</a>`)
)

func link(re *regexp.Regexp, body string) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func titles(body string) []string {
	var out []string
	for _, m := range titleRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestJournalKeysetPagination(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := range 120 {
		item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, fmt.Sprintf("Item %03d", i))
		// Pairs share a timestamp so the public_id tiebreak is exercised.
		record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbAdded, MediaItemID: item.ID, OccurredAt: base.Add(time.Duration(i/2) * time.Hour)})
	}
	c := s.LoginAs(t, alice)

	seen := map[string]bool{}
	var pages []string
	for path := "/journal?kind=book"; path != ""; path = link(nextLink, pages[len(pages)-1]) {
		res := c.Get(path)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, res.StatusCode)
		}
		if !strings.Contains(path, "kind=book") {
			t.Fatalf("link drops the filter: %s", path)
		}
		pages = append(pages, res.Body)
		for _, title := range titles(res.Body) {
			if seen[title] {
				t.Fatalf("%s repeated across pages", title)
			}
			seen[title] = true
		}
	}
	if len(pages) != 3 || len(seen) != 120 {
		t.Fatalf("pages=%d seen=%d", len(pages), len(seen))
	}
	if got := titles(pages[0])[0]; got != "Item 119" {
		t.Errorf("first event = %s, want newest", got)
	}
	if link(prevLink, pages[0]) != "" {
		t.Error("first page has a previous link")
	}
	prev := link(prevLink, pages[2])
	if prev == "" {
		t.Fatal("last page has no previous link")
	}
	res := c.Get(prev)
	if strings.Join(titles(res.Body), ",") != strings.Join(titles(pages[1]), ",") {
		t.Error("previous page differs from page 2")
	}
	if link(prevLink, res.Body) == "" {
		t.Error("page 2 has no previous link")
	}
}

func TestJournalCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	shared := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Hades")
	secret := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Bobs Secret Book")
	record(t, s, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbAdded, MediaItemID: shared.ID})
	list := testutil.CreateList(t, s.Pool, bob.ID, "Bobs List", db.VisibilityPublic, shared.ID)
	for _, v := range db.AllActivityVerbValues() {
		record(t, s, activity.Event{UserID: bob.ID, Verb: v, MediaItemID: secret.ID, Visibility: db.VisibilityPublic})
	}
	record(t, s, activity.Event{UserID: bob.ID, Verb: db.ActivityVerbListed, MediaItemID: shared.ID, ListID: &list.ID, Visibility: db.VisibilityPublic})

	res := s.LoginAs(t, alice).Get("/journal")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if strings.Contains(res.Body, "Bobs") {
		t.Error("bob's activity appears in alice's journal")
	}
	if n := strings.Count(res.Body, "data-verb="); n != 1 {
		t.Errorf("alice sees %d events, want 1", n)
	}
}
