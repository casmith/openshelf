package suggestions_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

// ttb stores a time-to-beat estimate in hours; completely < 0 leaves it null.
func (f *fixture) ttb(item db.MediaItem, normally, completely float64) {
	f.t.Helper()
	var c *int
	if completely >= 0 {
		m := int(completely * 60)
		c = &m
	}
	f.exec(`insert into game_time_to_beat (media_item_id, normally_minutes, completely_minutes) values ($1, $2, $3)`,
		item.ID, int(normally*60), c)
}

// played creates a game with no achievements, playtime in hours and an estimate.
func (f *fixture) played(account db.ConnectedAccount, title string, hours, normally, completely float64) db.MediaItem {
	f.t.Helper()
	item := f.game(account, title, int(hours*60))
	f.ttb(item, normally, completely)
	return item
}

func (f *fixture) lastPlayed(account db.ConnectedAccount, item db.MediaItem, at string) {
	f.t.Helper()
	f.exec(`update external_playtime set last_played_at = $3::timestamptz where account_id = $1 and media_item_id = $2`, account.ID, item.ID, at)
}

func sorted(ss []string) string {
	ss = append([]string(nil), ss...)
	sort.Strings(ss)
	return strings.Join(ss, ",")
}

func TestMaybeFromPlaytime(t *testing.T) {
	f, alice, acct := setup(t)
	at := f.played(acct, "At Main", 5, 5, 10)
	above := f.played(acct, "Above Main", 7, 5, 10)
	f.played(acct, "Below Main", 4.9, 5, 10)
	f.played(acct, "Valheim", 1671, 25, 122)
	noComp := f.played(acct, "No Completionist", 14.9, 5, -1)
	f.played(acct, "No Completionist Endless", 15, 5, -1)
	f.played(acct, "Tiny Estimate", 1, 0.4, 2)
	noEst := f.game(acct, "No Estimate", 600)
	f.exec(`insert into game_time_to_beat (media_item_id) values ($1)`, noEst.ID)
	quiet := f.game(acct, "Quiet Achievements", 600, unlock{"Jump", "Jump once", "2021-01-01T10:00:00Z"})
	f.ttb(quiet, 8, 20)
	portal := f.game(acct, "Portal", 600, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	f.ttb(portal, 3, 6)
	celeste := f.game(acct, "Celeste", 600, allUnlocked(6, "2022-03-04T10:00:00Z")...)
	f.ttb(celeste, 8, 20)

	c := f.s.LoginAs(t, alice)
	body := c.Get("/suggestions/finished").Body
	want := []string{
		"strong:" + portal.PublicID.String(), "medium:" + celeste.PublicID.String(),
		"maybe:" + at.PublicID.String(), "maybe:" + above.PublicID.String(),
		"maybe:" + noComp.PublicID.String(), "maybe:" + quiet.PublicID.String(),
	}
	if got := listed(body); sorted(got) != sorted(want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if !strings.Contains(body, "You played 7 h · most players finish the main story in 5 h") ||
		!strings.Contains(body, "You played 10 h · most players finish the main story in 8 h") {
		t.Errorf("missing playtime line\n%s", body)
	}
	if !strings.Contains(body, `<details data-maybe-group>`) || !strings.Contains(body, "(4)") {
		t.Error("maybe group should be collapsed with its count")
	}
	if i, j := strings.Index(body, portal.PublicID.String()), strings.Index(body, "data-maybe-group"); i > j {
		t.Error("maybe group should follow strong")
	}
	if !strings.Contains(c.Get("/").Body, `data-suggestions-count="6"`) {
		t.Error("dashboard count should include maybe games")
	}
	confirm := c.Get("/suggestions/finished/all").Body
	if strings.Contains(confirm, above.PublicID.String()) || !strings.Contains(confirm, "Mark 1 game as finished") {
		t.Error("mark all should stay strong-only")
	}
}

func TestMaybeDatedToLastPlayed(t *testing.T) {
	f, alice, acct := setup(t)
	second := testutil.CreateConnectedAccount(t, f.s.Pool, alice.ID, "steam", "76561190000000003")
	game := f.played(acct, "Hades", 30, 22, 95)
	f.lastPlayed(acct, game, "2023-01-01T10:00:00Z")
	f.exec(`insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total, last_played_at)
	        values ($1, $2, 'other', 10, '2024-03-04T03:00:00Z')`, second.ID, game.ID)
	c := f.s.LoginAs(t, alice)
	body := c.Get("/suggestions/finished").Body
	if !strings.Contains(body, "Dated to when you last played (Mar 3, 2024).") ||
		!strings.Contains(body, `action="/suggestions/finished/`+game.PublicID.String()+`/finish"`) {
		t.Fatalf("card missing dated finish\n%s", body)
	}
	if !strings.Contains(body, "You played 30 h ·") {
		t.Error("playtime should sum across the viewer's accounts")
	}
	c.PostForm("/suggestions/finished/"+game.PublicID.String()+"/finish", nil)
	if n := f.count(`select count(*) from experiences where user_id = $1 and media_item_id = $2 and state = 'finished' and finished_on = '2024-03-03'`, alice.ID, game.ID); n != 1 {
		t.Fatalf("%d experiences finished on Mar 3, 2024", n)
	}
	if n := f.count(`select count(*) from activity_events where user_id = $1 and verb = 'finished'
	                 and to_char(occurred_at at time zone 'America/Los_Angeles', 'YYYY-MM-DD') = '2024-03-03'`, alice.ID); n != 1 {
		t.Fatalf("%d finish events on Mar 3, 2024", n)
	}
}

func TestMaybeWithoutLastPlayedIsDateUnknownOnly(t *testing.T) {
	f, alice, acct := setup(t)
	game := f.played(acct, "Hades", 30, 22, 95)
	c := f.s.LoginAs(t, alice)
	body := c.Get("/suggestions/finished").Body
	prefix := `action="/suggestions/finished/` + game.PublicID.String()
	if strings.Contains(body, prefix+`/finish"`) || strings.Contains(body, "data-dated") ||
		!strings.Contains(body, prefix+`/finish-undated"`) || !strings.Contains(body, prefix+`/dismiss"`) {
		t.Fatalf("card should offer only date unknown and not finished\n%s", body)
	}
	if strings.Contains(body, "Achievements 0/0") {
		t.Error("maybe card shows an achievement count")
	}
	c.PostForm("/suggestions/finished/"+game.PublicID.String()+"/finish-undated", nil)
	if n := f.count(`select count(*) from experiences where user_id = $1 and state = 'finished' and finished_on is null`, alice.ID); n != 1 {
		t.Fatalf("%d undated finishes", n)
	}
	if n := f.count(`select count(*) from activity_events where user_id = $1`, alice.ID); n != 0 {
		t.Fatalf("%d activity events", n)
	}
}

func TestMaybeItemPanelAndDismiss(t *testing.T) {
	f, alice, acct := setup(t)
	game := f.played(acct, "Hades", 30, 22, 95)
	c := f.s.LoginAs(t, alice)
	item := "/items/" + game.PublicID.String()
	if body := c.Get(item).Body; !strings.Contains(body, "Your Steam playtime suggests you may have finished this") {
		t.Fatal("item page missing maybe suggestion")
	}
	if res := c.Form(http.MethodPost, "/suggestions/finished/"+game.PublicID.String()+"/dismiss", nil, true); res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 0 {
		t.Fatalf("dismissed game listed: %v", got)
	}
	if strings.Contains(c.Get(item).Body, `id="finish-suggestion"`) {
		t.Error("dismissed game still suggested on the item page")
	}
}

func TestMaybeCrossUserIsolation(t *testing.T) {
	f, alice, acct := setup(t)
	bob := testutil.CreateUser(t, f.s.Q, "bob")
	bobAcct := testutil.CreateConnectedAccount(t, f.s.Pool, bob.ID, "steam", "76561190000000002")
	shared := f.played(acct, "Hades", 10, 22, 95)
	f.exec(`insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total, last_played_at)
	        values ($1, $2, 'bob', 3000, now())`, bobAcct.ID, shared.ID)
	bobOnly := f.played(bobAcct, "Celeste", 30, 8, 40)
	c := f.s.LoginAs(t, alice)
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 0 {
		t.Fatalf("alice sees %v", got)
	}
	if strings.Contains(c.Get("/").Body, "data-suggestions-count") {
		t.Fatal("alice's count includes bob's playtime")
	}
	for _, it := range []db.MediaItem{shared, bobOnly} {
		if res := c.PostForm("/suggestions/finished/"+it.PublicID.String()+"/finish-undated", nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", it.Title, res.StatusCode)
		}
	}
	if got := listed(f.s.LoginAs(t, bob).Get("/suggestions/finished").Body); len(got) != 2 {
		t.Fatalf("bob sees %v", got)
	}
}
