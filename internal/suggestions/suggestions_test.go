package suggestions_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

type fixture struct {
	t *testing.T
	s *testutil.Server
	n int
}

type unlock struct {
	name, desc string
	at         string // RFC 3339; "" leaves the achievement locked
}

// game creates a Steam game with achievements for account, some unlocked, plus playtime.
func (f *fixture) game(account db.ConnectedAccount, title string, minutes int, achs ...unlock) db.MediaItem {
	f.t.Helper()
	f.n++
	ctx := context.Background()
	item := testutil.CreateItem(f.t, f.s.Pool, db.MediaKindGame, title)
	key := fmt.Sprintf("app%d", f.n)
	for i, a := range achs {
		var id int64
		err := f.s.Pool.QueryRow(ctx,
			`insert into achievements (media_item_id, provider, provider_game_key, provider_achievement_key, name, description, display_order)
			 values ($1, 'steam', $2, $3, $4, nullif($5, ''), $6)
			 on conflict (provider, provider_game_key, provider_achievement_key) do update set name = excluded.name
			 returning id`,
			item.ID, key, fmt.Sprintf("a%d", i), a.name, a.desc, i).Scan(&id)
		if err != nil {
			f.t.Fatal(err)
		}
		if a.at != "" {
			f.exec(`insert into achievement_unlocks (account_id, achievement_id, unlocked_at) values ($1, $2, $3::timestamptz)`, account.ID, id, a.at)
		}
	}
	f.exec(`insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total) values ($1, $2, $3, $4)`,
		account.ID, item.ID, key, minutes)
	return item
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.s.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func allUnlocked(n int, last string) []unlock {
	out := make([]unlock, n)
	for i := range out {
		out[i] = unlock{name: fmt.Sprintf("Thing %d", i+1), at: "2022-01-01T10:00:00Z"}
	}
	out[n-1] = unlock{name: "Collector", desc: "Get everything", at: last}
	return out
}

func setup(t *testing.T) (*fixture, db.User, db.ConnectedAccount) {
	t.Helper()
	s := testutil.NewServer(t)
	f := &fixture{t: t, s: s}
	alice := testutil.CreateUser(t, s.Q, "alice")
	f.exec(`update users set timezone = 'America/Los_Angeles', default_visibility = 'public' where id = $1`, alice.ID)
	return f, alice, testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "76561190000000001")
}

var levelRe = regexp.MustCompile(`data-suggestion="([^"]+)" data-level="(\w+)"`)

// listed returns "level:publicID" in page order.
func listed(body string) []string {
	var out []string
	for _, m := range levelRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[2]+":"+m[1])
	}
	return out
}

func TestPageGroupsStrongThenMedium(t *testing.T) {
	f, alice, acct := setup(t)
	celeste := f.game(acct, "Celeste", 1300, allUnlocked(6, "2022-03-04T10:00:00Z")...)
	portal := f.game(acct, "Portal", 240,
		unlock{"Lab Rat", "Maneuver inside a Weighted Storage Cube", "2021-11-20T10:00:00Z"},
		unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"},
		unlock{"Fratricide", "Do whatever it takes to survive", ""})
	f.game(acct, "Quake", 90,
		unlock{"Sink the Horror Electric", "Complete Quake Episode 1: Dimension of the Doomed in single player.", "2021-05-01T10:00:00Z"},
		unlock{"Boss", "Defeat the final boss", ""})
	done := f.game(acct, "Finished Already", 60, unlock{"The End", "See the epilogue", "2021-01-01T10:00:00Z"})
	testutil.CreateExperience(t, f.s.Pool, alice.ID, done.ID, db.ExperienceStateFinished)
	journey := f.game(acct, "Journey", 200, unlock{"Long, Strange Trip", "Complete all Areas of Journey Mode", "2020-02-02T10:00:00Z"})

	res := f.s.LoginAs(t, alice).Get("/suggestions/finished")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	want := []string{"strong:" + portal.PublicID.String(), "medium:" + celeste.PublicID.String(), "medium:" + journey.PublicID.String()}
	if got := listed(res.Body); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("listed %v, want %v", got, want)
	}
	for _, s := range []string{
		"Heartbreaker — Complete Portal · unlocked Nov 26, 2021",
		"Achievements 2/3",
		"4.0 h played",
		"Achievements 6/6",
		"Collector — Get everything · unlocked Mar 4, 2022",
		"Nothing is marked until you confirm.",
	} {
		if !strings.Contains(res.Body, s) {
			t.Errorf("page missing %q", s)
		}
	}
}

func TestEmptyStateWithoutAccounts(t *testing.T) {
	s := testutil.NewServer(t)
	bob := testutil.CreateUser(t, s.Q, "bob")
	res := s.LoginAs(t, bob).Get("/suggestions/finished")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `href="/settings/connections"`) || !strings.Contains(res.Body, "No linked accounts") {
		t.Fatalf("status %d\n%s", res.StatusCode, res.Body)
	}
}

func TestMarkFinishedUsesEvidenceDayInViewerTimezone(t *testing.T) {
	f, alice, acct := setup(t)
	portal := f.game(acct, "Portal", 240,
		unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"},
		unlock{"The End", "See the epilogue", "2021-12-30T05:00:00Z"})
	c := f.s.LoginAs(t, alice)
	res := c.Form(http.MethodPost, "/suggestions/finished/"+portal.PublicID.String()+"/finish", nil, true)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(res.Body) != "" {
		t.Fatalf("status %d body %q", res.StatusCode, res.Body)
	}
	var finished time.Time
	var vis string
	err := f.s.Pool.QueryRow(context.Background(),
		`select finished_on, visibility::text from experiences where user_id = $1 and media_item_id = $2 and state = 'finished'`,
		alice.ID, portal.ID).Scan(&finished, &vis)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Format("2006-01-02") != "2021-11-26" || vis != "public" {
		t.Fatalf("finished_on %s visibility %s", finished.Format("2006-01-02"), vis)
	}
	var day string
	err = f.s.Pool.QueryRow(context.Background(),
		`select to_char(occurred_at at time zone 'America/Los_Angeles', 'YYYY-MM-DD') from activity_events
		 where user_id = $1 and media_item_id = $2 and verb = 'finished'`, alice.ID, portal.ID).Scan(&day)
	if err != nil {
		t.Fatal(err)
	}
	if day != "2021-11-26" {
		t.Fatalf("activity on %s", day)
	}
	if body := c.Get("/suggestions/finished").Body; strings.Contains(body, portal.PublicID.String()) {
		t.Fatal("finished game still suggested")
	}
}

func TestFinishedDateUnknownRecordsNoActivity(t *testing.T) {
	f, alice, acct := setup(t)
	portal := f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	res := f.s.LoginAs(t, alice).PostForm("/suggestions/finished/"+portal.PublicID.String()+"/finish-undated", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/suggestions/finished" {
		t.Fatalf("status %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
	if n := f.count(`select count(*) from experiences where user_id = $1 and media_item_id = $2 and state = 'finished' and finished_on is null`, alice.ID, portal.ID); n != 1 {
		t.Fatalf("%d undated finished experiences", n)
	}
	if n := f.count(`select count(*) from activity_events where user_id = $1`, alice.ID); n != 0 {
		t.Fatalf("%d activity events", n)
	}
}

func TestDismissHidesPermanently(t *testing.T) {
	f, alice, acct := setup(t)
	portal := f.game(acct, "Portal", 240,
		unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"},
		unlock{"The End", "See the epilogue", ""})
	c := f.s.LoginAs(t, alice)
	res := c.Form(http.MethodPost, "/suggestions/finished/"+portal.PublicID.String()+"/dismiss", nil, true)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	f.exec(`insert into achievement_unlocks (account_id, achievement_id, unlocked_at)
	        select $1, id, now() from achievements where media_item_id = $2 and name = 'The End'`, acct.ID, portal.ID)
	if body := c.Get("/suggestions/finished").Body; len(listed(body)) != 0 {
		t.Fatalf("dismissed game listed: %v", listed(body))
	}
	if n := f.count(`select count(*) from experiences where user_id = $1`, alice.ID); n != 0 {
		t.Fatalf("dismiss created %d experiences", n)
	}
	if res := c.PostForm("/suggestions/finished/"+portal.PublicID.String()+"/finish", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("finishing a dismissed suggestion: status %d", res.StatusCode)
	}
}

func TestMarkAllStrong(t *testing.T) {
	f, alice, acct := setup(t)
	portal := f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	gris := f.game(acct, "Gris", 240, unlock{"The End", "See the epilogue", "2022-06-01T20:00:00Z"})
	celeste := f.game(acct, "Celeste", 1300, allUnlocked(6, "2022-03-04T10:00:00Z")...)
	c := f.s.LoginAs(t, alice)
	confirm := c.Get("/suggestions/finished/all")
	for _, id := range []string{portal.PublicID.String(), gris.PublicID.String()} {
		if !strings.Contains(confirm.Body, `data-confirm="`+id+`"`) {
			t.Errorf("confirm page missing %s", id)
		}
	}
	if strings.Contains(confirm.Body, celeste.PublicID.String()) || !strings.Contains(confirm.Body, "Mark 2 games as finished") {
		t.Error("confirm page should list only the strong games")
	}
	if n := f.count(`select count(*) from experiences where user_id = $1`, alice.ID); n != 0 {
		t.Fatal("confirmation page wrote experiences")
	}
	res := c.PostForm("/suggestions/finished/all", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", res.StatusCode)
	}
	if n := f.count(`select count(*) from experiences where user_id = $1 and state = 'finished' and finished_on is not null`, alice.ID); n != 2 {
		t.Fatalf("%d dated finishes", n)
	}
	if n := f.count(`select count(*) from activity_events where user_id = $1 and verb = 'finished'`, alice.ID); n != 2 {
		t.Fatalf("%d finish events", n)
	}
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 1 || got[0] != "medium:"+celeste.PublicID.String() {
		t.Fatalf("after bulk: %v", got)
	}
}

func TestEntryPointsAndItemPanel(t *testing.T) {
	f, alice, acct := setup(t)
	c := f.s.LoginAs(t, alice)
	for _, p := range []string{"/", "/library"} {
		if strings.Contains(c.Get(p).Body, "data-suggestions-count") {
			t.Fatalf("%s shows a link with no suggestions", p)
		}
	}
	portal := f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	f.game(acct, "Celeste", 1300, allUnlocked(6, "2022-03-04T10:00:00Z")...)
	quake := f.game(acct, "Quake", 90, unlock{"E1M1", "Complete the first mission of the campaign", "2021-05-01T10:00:00Z"})
	for _, p := range []string{"/", "/library", "/library/game"} {
		body := c.Get(p).Body
		if !strings.Contains(body, `data-suggestions-count="2"`) || !strings.Contains(body, "2 games look finished") {
			t.Errorf("%s: missing count link", p)
		}
	}
	if strings.Contains(c.Get("/library/book").Body, "data-suggestions-count") {
		t.Error("book library shows the games link")
	}

	item := "/items/" + portal.PublicID.String()
	if body := c.Get(item).Body; !strings.Contains(body, "Steam suggests you finished this") {
		t.Fatal("item page missing suggestion")
	}
	if body := c.Get("/items/" + quake.PublicID.String()).Body; strings.Contains(body, "Steam suggests you finished this") {
		t.Fatal("non-suggestion shows the panel")
	}
	res := c.PostForm("/suggestions/finished/"+portal.PublicID.String()+"/finish", url.Values{"next": {item}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != item {
		t.Fatalf("status %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
	if body := c.Get(item).Body; strings.Contains(body, "Steam suggests you finished this") {
		t.Fatal("panel still shown after finishing")
	}
	if !strings.Contains(c.Get("/").Body, "1 game looks finished") {
		t.Error("dashboard count did not drop")
	}
}

func TestCrossUserIsolation(t *testing.T) {
	f, alice, acct := setup(t)
	portal := f.game(acct, "Portal", 240, unlock{"Heartbreaker", "Complete Portal", "2021-11-27T05:00:00Z"})
	bob := testutil.CreateUser(t, f.s.Q, "bob")
	testutil.CreateConnectedAccount(t, f.s.Pool, bob.ID, "steam", "76561190000000002")
	c := f.s.LoginAs(t, bob)
	if got := listed(c.Get("/suggestions/finished").Body); len(got) != 0 {
		t.Fatalf("bob sees %v", got)
	}
	if strings.Contains(c.Get("/").Body, "data-suggestions-count") {
		t.Fatal("bob's dashboard counts alice's suggestions")
	}
	if strings.Contains(c.Get("/items/"+portal.PublicID.String()).Body, "Steam suggests") {
		t.Fatal("bob sees alice's suggestion on the item page")
	}
	for _, a := range []string{"finish", "finish-undated", "dismiss"} {
		if res := c.PostForm("/suggestions/finished/"+portal.PublicID.String()+"/"+a, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", a, res.StatusCode)
		}
	}
	c.PostForm("/suggestions/finished/all", nil)
	if n := f.count(`select count(*) from experiences`); n != 0 {
		t.Fatalf("%d experiences created", n)
	}
	if n := f.count(`select count(*) from finish_suggestion_dismissals`); n != 0 {
		t.Fatalf("%d dismissals created", n)
	}
	if got := listed(f.s.LoginAs(t, alice).Get("/suggestions/finished").Body); len(got) != 1 {
		t.Fatalf("alice sees %v", got)
	}
}

func TestQueryCountsAreBounded(t *testing.T) {
	f, alice, acct := setup(t)
	c := f.s.LoginAs(t, alice)
	add := func(from, n int) {
		for i := from; i < from+n; i++ {
			switch i % 3 {
			case 0:
				f.game(acct, fmt.Sprintf("Game %03d", i), 60, unlock{"The End", "See the epilogue", "2021-11-27T05:00:00Z"}, unlock{"Other", "", "2021-11-01T05:00:00Z"})
			case 1:
				f.game(acct, fmt.Sprintf("Game %03d", i), 60, allUnlocked(5, "2022-03-04T10:00:00Z")...)
			default:
				f.game(acct, fmt.Sprintf("Game %03d", i), 60, unlock{"Chapter 1", "Finish chapter 1", "2021-11-27T05:00:00Z"})
			}
		}
	}
	add(0, 3)
	home := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/") })
	page := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/suggestions/finished") })
	add(3, 297)
	if got := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/") }); got != home {
		t.Errorf("home ran %d queries with 3 games and %d with 300", home, got)
	}
	res := c.Get("/suggestions/finished")
	if n := len(listed(res.Body)); n != 200 {
		t.Fatalf("listed %d suggestions", n)
	}
	if got := testutil.CountQueries(t, f.s.Pool, func() { c.Get("/suggestions/finished") }); got != page {
		t.Errorf("page ran %d queries with 3 games and %d with 300", page, got)
	}
}

func TestMediumIsDatedToLastAchievement(t *testing.T) {
	f, alice, acct := setup(t)
	journey := f.game(acct, "Journey", 200,
		unlock{"Long, Strange Trip", "Complete all Areas of Journey Mode", "2020-02-02T10:00:00Z"},
		unlock{"Companion", "Meet a companion", "2024-03-04T03:00:00Z"},
		unlock{"Transfiguration", "Reach the summit", ""})
	c := f.s.LoginAs(t, alice)
	body := c.Get("/suggestions/finished").Body
	if !strings.Contains(body, "Long, Strange Trip — Complete all Areas of Journey Mode · unlocked Feb 2, 2020") ||
		!strings.Contains(body, "Dated to your last achievement, Mar 3, 2024.") {
		t.Fatalf("card missing evidence or date\n%s", body)
	}
	c.PostForm("/suggestions/finished/"+journey.PublicID.String()+"/finish", nil)
	if n := f.count(`select count(*) from experiences where user_id = $1 and media_item_id = $2 and finished_on = '2024-03-03'`, alice.ID, journey.ID); n != 1 {
		t.Fatalf("%d experiences finished on Mar 3, 2024", n)
	}
}
