package suggestions_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

// book creates a book with Audiobookshelf progress for account; empty times are null.
func (f *fixture) book(account db.ConnectedAccount, title string, fraction float64, finished bool, finishedAt, lastActivity string) db.MediaItem {
	f.t.Helper()
	f.n++
	item := testutil.CreateItem(f.t, f.s.Pool, db.MediaKindBook, title)
	f.exec(`insert into external_progress (account_id, media_item_id, provider_item_key, fraction, finished, finished_at, last_activity_at)
	        values ($1, $2, $3, $4, $5, nullif($6, '')::timestamptz, nullif($7, '')::timestamptz)`,
		account.ID, item.ID, fmt.Sprintf("li_%d", f.n), fraction, finished, finishedAt, lastActivity)
	return item
}

func absAccount(t *testing.T, f *fixture, u db.User) db.ConnectedAccount {
	t.Helper()
	return testutil.CreateConnectedAccount(t, f.s.Pool, u.ID, "audiobookshelf", "abs-"+u.Username)
}

func TestListenedStrongAndMedium(t *testing.T) {
	f, alice, _ := setup(t)
	abs := absAccount(t, f, alice)
	kings := f.book(abs, "The Way of Kings", 1, true, "2026-03-04T03:00:00Z", "2026-03-04T03:00:00Z")
	undated := f.book(abs, "Undated", 1, true, "", "2026-02-01T10:00:00Z")
	nearly := f.book(abs, "Project Hail Mary", 0.9712, false, "", "2026-03-04T03:00:00Z")
	f.book(abs, "Halfway", 0.94, false, "", "2026-03-04T03:00:00Z")
	done := f.book(abs, "Already Read", 1, true, "2025-01-01T10:00:00Z", "")
	testutil.CreateExperience(t, f.s.Pool, alice.ID, done.ID, db.ExperienceStateFinished)
	dismissed := f.book(abs, "Not Really", 1, true, "2025-01-01T10:00:00Z", "")
	f.exec(`insert into finish_suggestion_dismissals (user_id, media_item_id) values ($1, $2)`, alice.ID, dismissed.ID)

	c := f.s.LoginAs(t, alice)
	body := c.Get("/suggestions/finished").Body
	want := []string{"strong:" + kings.PublicID.String(), "strong:" + undated.PublicID.String(), "medium:" + nearly.PublicID.String()}
	if got := listed(body); sorted(got) != sorted(want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	for _, s := range []string{
		"Based on your Steam achievements, playtime, and Audiobookshelf progress.",
		"Audiobookshelf says you finished this · Mar 3, 2026",
		"You listened to 97%",
		"Dated to when you last listened (Mar 3, 2026).",
		"placeholder-book",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("page missing %q", s)
		}
	}
	card := cardFor(body, undated.PublicID.String())
	if !strings.Contains(card, "Audiobookshelf says you finished this</p>") || strings.Contains(card, "/finish\"") {
		t.Errorf("undated finish should only offer date unknown:\n%s", card)
	}
	if strings.Contains(body, "Achievements 0/0") {
		t.Error("book card shows achievement counts")
	}

	for _, p := range []string{"/", "/library", "/library/book"} {
		if !strings.Contains(c.Get(p).Body, "3 books look finished") {
			t.Errorf("%s: missing book count link", p)
		}
	}
	if strings.Contains(c.Get("/library/game").Body, "data-suggestions-count") {
		t.Error("game library counts books")
	}
	if !strings.Contains(c.Get("/items/"+kings.PublicID.String()).Body, "Audiobookshelf says you finished this —") {
		t.Error("item page missing strong prompt")
	}
	if !strings.Contains(c.Get("/items/"+nearly.PublicID.String()).Body, "You listened to 97% on Audiobookshelf —") {
		t.Error("item page missing medium prompt")
	}

	c.PostForm("/suggestions/finished/"+kings.PublicID.String()+"/finish", nil)
	c.PostForm("/suggestions/finished/"+nearly.PublicID.String()+"/finish", nil)
	for _, it := range []db.MediaItem{kings, nearly} {
		if n := f.count(`select count(*) from experiences where user_id = $1 and media_item_id = $2 and state = 'finished' and finished_on = '2026-03-03'`, alice.ID, it.ID); n != 1 {
			t.Errorf("%s: %d finishes on Mar 3, 2026", it.Title, n)
		}
	}
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 1 {
		t.Fatalf("after finishing: %v", got)
	}
	if n := f.count(`select count(*) from external_progress where finished and fraction = 1`); n != 4 {
		t.Fatalf("suggestions changed external_progress: %d finished rows", n)
	}
}

func cardFor(body, id string) string {
	i := strings.Index(body, `data-suggestion="`+id+`"`)
	if i < 0 {
		return ""
	}
	end := strings.Index(body[i:], "</li>")
	return body[i : i+end]
}

func TestMarkAllStrongIncludesBooks(t *testing.T) {
	f, alice, acct := setup(t)
	abs := absAccount(t, f, alice)
	portal := f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	kings := f.book(abs, "The Way of Kings", 1, true, "2026-03-04T03:00:00Z", "")
	f.book(abs, "Project Hail Mary", 0.97, false, "", "2026-03-04T03:00:00Z")
	c := f.s.LoginAs(t, alice)
	confirm := c.Get("/suggestions/finished/all").Body
	if !strings.Contains(confirm, `data-confirm="`+kings.PublicID.String()+`"`) || !strings.Contains(confirm, `data-confirm="`+portal.PublicID.String()+`"`) ||
		!strings.Contains(confirm, "Mark 2 items as finished") {
		t.Fatalf("confirm page:\n%s", confirm)
	}
	if !strings.Contains(c.Get("/").Body, "3 items look finished") {
		t.Error("dashboard link should say items for mixed kinds")
	}
	if res := c.PostForm("/suggestions/finished/all", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", res.StatusCode)
	}
	if n := f.count(`select count(*) from experiences where user_id = $1 and media_item_id = $2 and finished_on = '2026-03-03'`, alice.ID, kings.ID); n != 1 {
		t.Fatalf("%d book finishes on Mar 3, 2026", n)
	}
	if n := f.count(`select count(*) from experiences where user_id = $1`, alice.ID); n != 2 {
		t.Fatalf("%d experiences; the medium book must not be marked", n)
	}
}

func TestListenedCrossUserIsolation(t *testing.T) {
	f, alice, _ := setup(t)
	abs := absAccount(t, f, alice)
	kings := f.book(abs, "The Way of Kings", 1, true, "2026-03-04T03:00:00Z", "")
	nearly := f.book(abs, "Project Hail Mary", 0.97, false, "", "2026-03-04T03:00:00Z")
	bob := testutil.CreateUser(t, f.s.Q, "bob")
	bobAbs := absAccount(t, f, bob)
	f.exec(`insert into external_progress (account_id, media_item_id, provider_item_key, fraction) values ($1, $2, 'li_bob', 0.2)`, bobAbs.ID, kings.ID)
	c := f.s.LoginAs(t, bob)
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 0 {
		t.Fatalf("bob sees %v", got)
	}
	if body := c.Get("/items/" + kings.PublicID.String()).Body; strings.Contains(body, "Audiobookshelf says") {
		t.Fatal("bob sees alice's suggestion on the item page")
	}
	for _, it := range []db.MediaItem{kings, nearly} {
		for _, a := range []string{"finish", "finish-undated", "dismiss"} {
			if res := c.PostForm("/suggestions/finished/"+it.PublicID.String()+"/"+a, nil); res.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: status %d", it.Title, a, res.StatusCode)
			}
		}
	}
	c.PostForm("/suggestions/finished/all", nil)
	if n := f.count(`select (select count(*) from experiences) + (select count(*) from finish_suggestion_dismissals)`); n != 0 {
		t.Fatalf("bob wrote %d rows", n)
	}
	if got := listed(f.s.LoginAs(t, alice).Get("/suggestions/finished").Body); len(got) != 2 {
		t.Fatalf("alice sees %v", got)
	}
}

func TestListenedQueryCountsAreBounded(t *testing.T) {
	f, alice, acct := setup(t)
	c := f.s.LoginAs(t, alice)
	f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	home := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/") })
	page := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/suggestions/finished") })
	abs := absAccount(t, f, alice)
	for i := range 60 {
		if i%2 == 0 {
			f.book(abs, fmt.Sprintf("Book %03d", i), 1, true, "2026-03-04T03:00:00Z", "")
		} else {
			f.book(abs, fmt.Sprintf("Book %03d", i), 0.96, false, "", "2026-03-04T03:00:00Z")
		}
	}
	if got := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/") }); got != home {
		t.Errorf("home ran %d queries without books and %d with 60", home, got)
	}
	if got := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/suggestions/finished") }); got != page {
		t.Errorf("page ran %d queries without books and %d with 60", page, got)
	}
	if n := len(listed(c.Get("/suggestions/finished").Body)); n != 61 {
		t.Fatalf("listed %d", n)
	}
}
