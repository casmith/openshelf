package dashboard_test

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/seed"
	"github.com/casmith/openshelf/internal/testutil"
)

func exec(t *testing.T, s *testutil.Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// testutil.CreateExperience fails on an untyped $3, so tests insert experiences directly.
func createExperience(t *testing.T, s *testutil.Server, userID, itemID int64, state db.ExperienceState) int64 {
	t.Helper()
	var id int64
	err := s.Pool.QueryRow(context.Background(),
		`insert into experiences (user_id, media_item_id, state, finished_on)
		 values ($1, $2, $3::experience_state, case when $3::experience_state in ('finished', 'abandoned') then current_date end)
		 returning id`, userID, itemID, state).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var counterRe = regexp.MustCompile(`id="count-([a-z-]+)">([\d,]+)<`)

func counters(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range counterRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	if len(out) != 4 {
		t.Fatalf("found %d counters", len(out))
	}
	return out
}

func sectionTitles(body, section string) []string {
	re := regexp.MustCompile(`(?s)<li[^>]*data-section="` + section + `".*?font-medium group-hover:text-accent">([^<]+)</p>`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func loginSeeded(t *testing.T, s *testutil.Server, username string) (db.User, *testutil.Client) {
	t.Helper()
	hash, err := auth.HashPassword(testutil.TestPassword)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, "update users set password_hash = $1 where username = $2", hash, username)
	u, err := s.Q.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return u, s.LoginAs(t, u)
}

// expectedCounters computes the derived definitions in Go from raw rows.
func expectedCounters(t *testing.T, s *testutil.Server, userID int64) map[string]string {
	t.Helper()
	ctx := context.Background()
	inLibrary, backlogFlag := map[int64]bool{}, map[int64]bool{}
	rows, err := s.Pool.Query(ctx, "select media_item_id, backlog from library_entries where user_id = $1", userID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var b bool
		_ = rows.Scan(&id, &b)
		inLibrary[id] = true
		backlogFlag[id] = backlogFlag[id] || b
	}
	rows.Close()
	inProgress, finished := map[int64]bool{}, map[int64]bool{}
	rows, err = s.Pool.Query(ctx, "select media_item_id, state::text from experiences where user_id = $1", userID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var st string
		_ = rows.Scan(&id, &st)
		switch st {
		case "active", "paused":
			inProgress[id] = true
		case "finished":
			finished[id] = true
		}
	}
	rows.Close()
	backlog := 0
	for id, b := range backlogFlag {
		if b && !inProgress[id] && !finished[id] {
			backlog++
		}
	}
	return map[string]string{
		"cataloged":   fmt.Sprint(len(inLibrary)),
		"in-progress": fmt.Sprint(len(inProgress)),
		"backlog":     fmt.Sprint(backlog),
		"completed":   fmt.Sprint(len(finished)),
	}
}

func TestHomeOnSeedData(t *testing.T) {
	s := testutil.NewServer(t)
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)
	if _, err := seed.Run(context.Background(), s.Pool, now); err != nil {
		t.Fatal(err)
	}
	s.Deps.Now = func() time.Time { return now }
	alice, c := loginSeeded(t, s, "alice")

	res := c.Get("/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	got, want := counters(t, res.Body), expectedCounters(t, s, alice.ID)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if want["backlog"] == "0" || want["completed"] == "0" || want["in-progress"] == "0" {
		t.Fatalf("seed data no longer exercises every counter: %v", want)
	}
	// 15:00 UTC is 10:00 in America/New_York.
	if !strings.Contains(res.Body, "Good morning, Alice") {
		t.Error("greeting not in the viewer's timezone")
	}

	current := sectionTitles(res.Body, "current")
	if len(current) != 5 || current[0] == "Disco Elysium" || !contains(current, "Valheim") || !contains(current, "Disco Elysium") {
		t.Errorf("currently enjoying = %v", current)
	}
	finished := sectionTitles(res.Body, "finished")
	if len(finished) != 6 || !unique(finished) {
		t.Errorf("recently finished = %v", finished)
	}
	backlog := sectionTitles(res.Body, "backlog")
	if len(backlog) != 3 || !unique(backlog) {
		t.Errorf("backlog picks = %v", backlog)
	}
	for _, b := range backlog {
		if b == "Hollow Knight" || b == "Valheim" {
			t.Errorf("non-backlog item %s picked", b)
		}
	}
	if n := strings.Count(res.Body, `data-section="activity"`); n != 8 {
		t.Errorf("activity feed has %d events", n)
	}
	if !strings.Contains(res.Body, `aria-valuenow="72"`) || !strings.Contains(res.Body, "width: 72%") {
		t.Error("missing progress bar for Pandora's Star (1079/1496)")
	}
}

func TestCountersFollowDerivedDefinitions(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := func(title string) int64 { return testutil.CreateItem(t, s.Pool, db.MediaKindGame, title).ID }
	backlogged := func(id int64) {
		exec(t, s, "insert into library_entries (user_id, media_item_id, backlog) values ($1, $2, true)", alice.ID, id)
	}

	plain := item("Plain")
	backlogged(plain)
	active := item("Active backlog")
	backlogged(active)
	createExperience(t, s, alice.ID, active, db.ExperienceStateActive)
	done := item("Finished backlog")
	backlogged(done)
	createExperience(t, s, alice.ID, done, db.ExperienceStateFinished)
	createExperience(t, s, alice.ID, done, db.ExperienceStateFinished)
	dropped := item("Abandoned backlog")
	backlogged(dropped)
	createExperience(t, s, alice.ID, dropped, db.ExperienceStateAbandoned)
	twoEditions := item("Two editions")
	exec(t, s, "insert into library_entries (user_id, media_item_id) values ($1, $2)", alice.ID, twoEditions)
	ed := testutil.CreateEdition(t, s.Pool, twoEditions, "pc")
	exec(t, s, "insert into library_entries (user_id, media_item_id, edition_id) values ($1, $2, $3)", alice.ID, twoEditions, ed.ID)

	res := s.LoginAs(t, alice).Get("/")
	got := counters(t, res.Body)
	want := map[string]string{"cataloged": "5", "in-progress": "1", "backlog": "2", "completed": "1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	picks := sectionTitles(res.Body, "backlog")
	if len(picks) != 2 || !contains(picks, "Plain") || !contains(picks, "Abandoned backlog") {
		t.Errorf("backlog picks = %v", picks)
	}
}

func TestRecentlyFinishedSkipsUndated(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	dated := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Dated Game").ID
	createExperience(t, s, alice.ID, dated, db.ExperienceStateFinished)
	undated := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Undated Game").ID
	id := createExperience(t, s, alice.ID, undated, db.ExperienceStateFinished)
	exec(t, s, "update experiences set finished_on = null where id = $1", id)

	res := s.LoginAs(t, alice).Get("/")
	if finished := sectionTitles(res.Body, "finished"); len(finished) != 1 || finished[0] != "Dated Game" {
		t.Errorf("recently finished = %v, want only the dated finish", finished)
	}
	if got := counters(t, res.Body)["completed"]; got != "2" {
		t.Errorf("completed = %s, want 2 (undated finishes still count)", got)
	}
}

func TestBacklogPicksStablePerDay(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s, "update users set timezone = 'America/New_York' where id = $1", alice.ID)
	for i := range 12 {
		id := testutil.CreateItem(t, s.Pool, db.MediaKindBook, fmt.Sprintf("Book %02d", i)).ID
		exec(t, s, "insert into library_entries (user_id, media_item_id, backlog) values ($1, $2, true)", alice.ID, id)
	}
	c := s.LoginAs(t, alice)
	picksAt := func(at time.Time) string {
		s.Deps.Now = func() time.Time { return at }
		return strings.Join(sectionTitles(c.Get("/").Body, "backlog"), ",")
	}
	// Both instants fall on Nov 4 in New York, though the second is Nov 5 in UTC.
	morning := picksAt(time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC))
	night := picksAt(time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC))
	if morning != night {
		t.Fatalf("picks changed within a day: %q vs %q", morning, night)
	}
	changed := false
	for d := 1; d <= 10 && !changed; d++ {
		changed = picksAt(time.Date(2026, 11, 4+d, 13, 0, 0, 0, time.UTC)) != morning
	}
	if !changed {
		t.Error("picks never changed across ten days")
	}
}

func TestHomeQueryCountIsBounded(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	addItems := func(n int, offset int) {
		for i := range n {
			id := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, fmt.Sprintf("Film %03d", offset+i)).ID
			exec(t, s, "insert into library_entries (user_id, media_item_id, backlog) values ($1, $2, $3)", alice.ID, id, i%2 == 0)
			createExperience(t, s, alice.ID, id, db.ExperienceStateFinished)
			if _, err := activity.Record(context.Background(), s.Q, activity.Event{UserID: alice.ID, Verb: db.ActivityVerbFinished, MediaItemID: id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	active := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Active").ID
	createExperience(t, s, alice.ID, active, db.ExperienceStateActive)
	addItems(2, 0)
	small := testutil.CountQueries(t, s.Pool, func() { c.Get("/") })
	addItems(40, 100)
	large := testutil.CountQueries(t, s.Pool, func() { c.Get("/") })
	if small != large {
		t.Fatalf("home ran %d queries with a small library and %d with a large one", small, large)
	}
}

func TestHomeCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	for i, st := range []db.ExperienceState{db.ExperienceStateActive, db.ExperienceStateFinished, ""} {
		id := testutil.CreateItem(t, s.Pool, db.MediaKindGame, fmt.Sprintf("Bobs Game %d", i)).ID
		exec(t, s, "insert into library_entries (user_id, media_item_id, backlog, visibility) values ($1, $2, true, 'public')", bob.ID, id)
		if st != "" {
			createExperience(t, s, bob.ID, id, st)
		}
		if _, err := activity.Record(context.Background(), s.Q, activity.Event{UserID: bob.ID, Verb: db.ActivityVerbAdded, MediaItemID: id, Visibility: db.VisibilityPublic}); err != nil {
			t.Fatal(err)
		}
	}
	res := s.LoginAs(t, alice).Get("/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if strings.Contains(res.Body, "Bobs Game") {
		t.Error("bob's data appears on alice's home")
	}
	for k, v := range counters(t, res.Body) {
		if v != "0" {
			t.Errorf("alice's %s counter = %s", k, v)
		}
	}
	if res := s.LoginAs(t, alice).Get("/journal"); strings.Contains(res.Body, "Bobs Game") {
		t.Error("bob's activity appears in alice's journal")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func unique(xs []string) bool {
	seen := map[string]bool{}
	for _, x := range xs {
		if seen[x] {
			return false
		}
		seen[x] = true
	}
	return true
}
