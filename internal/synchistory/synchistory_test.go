package synchistory_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

type env struct {
	s     *testutil.Server
	fake  *synctest.Fake
	alice db.User
	acct  db.ConnectedAccount
	c     *testutil.Client
}

func setup(t *testing.T) *env {
	t.Helper()
	s := testutil.NewServer(t)
	f := &synctest.Fake{Name: "steam", Caps: sync.Capabilities{DisplayName: "Steam", OwnedGames: true, Playtime: true, Achievements: true}}
	s.Deps.SyncAdapters = sync.NewRegistry(f)
	alice := testutil.CreateUser(t, s.Q, "alice")
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "76561197960287930")
	return &env{s: s, fake: f, alice: alice, acct: acct, c: s.LoginAs(t, alice)}
}

func (e *env) sync(t *testing.T) runner.Result {
	t.Helper()
	return syncAccount(t, e.s, e.acct)
}

func syncAccount(t *testing.T, s *testutil.Server, acct db.ConnectedAccount) runner.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), s.Deps, acct.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("sync error: %v", res.Err)
	}
	return res
}

func ip(v int) *int { return &v }

func game(key, title string, minutes int) sync.OwnedGame {
	return sync.OwnedGame{
		Key: key, Title: title, PlaytimeMinutes: ip(minutes),
		ExternalIDs: []providers.ExternalID{{Provider: "steam_app", Value: key, URL: "https://store.steampowered.com/app/" + key}},
	}
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func itemFor(t *testing.T, s *testutil.Server, key string) uuid.UUID {
	t.Helper()
	return scalar[uuid.UUID](t, s.Pool, `select m.public_id from external_ids x join media_items m on m.id = x.media_item_id
		where x.provider = 'steam_app' and x.value = $1`, key)
}

func entryFor(t *testing.T, s *testutil.Server, userID int64, key string) uuid.UUID {
	t.Helper()
	return scalar[uuid.UUID](t, s.Pool, `select le.public_id from library_entries le
		join external_ids x on x.media_item_id = le.media_item_id and x.provider = 'steam_app' and x.value = $2
		where le.user_id = $1`, userID, key)
}

func entryCount(t *testing.T, s *testutil.Server, userID int64, key string) int {
	t.Helper()
	return scalar[int](t, s.Pool, `select count(*) from library_entries le
		join external_ids x on x.media_item_id = le.media_item_id and x.provider = 'steam_app' and x.value = $2
		where le.user_id = $1`, userID, key)
}

func want(t *testing.T, res *testutil.Response, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, res.Body)
	}
}

func contains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("page missing %q", w)
		}
	}
}

var tags = regexp.MustCompile(`<[^>]*>`)

// text strips tags and collapses whitespace so assertions read like the page.
func text(html string) string {
	return strings.Join(strings.Fields(tags.ReplaceAllString(html, " ")), " ")
}

func (e *env) historyURL() string {
	return "/settings/connections/" + e.acct.PublicID.String() + "/history"
}

// README acceptance test: Portal 2, end to end over HTTP.
func TestPortal2OverrideRespected(t *testing.T) {
	e := setup(t)
	set := sync.AchievementSet{
		Key:          "620",
		Achievements: []sync.Achievement{{Key: "a1", Name: "Wake Up Call"}, {Key: "a2", Name: "You Monster"}, {Key: "a3", Name: "Undiscouraged"}},
		Unlocks:      []sync.Unlock{{Key: "a1"}},
	}
	// 1. Import a library that includes Portal 2 with 30 h.
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 1800)}
	e.fake.Sets = []sync.AchievementSet{set}
	e.sync(t)
	item := "/items/" + itemFor(t, e.s, "620").String()
	page := text(e.c.Get(item).Body)
	contains(t, page, "Owned · from Steam", "Played · inferred from Steam", "Playtime 30 h", "Achievements 1 / 3")

	// 2. Mark Portal 2 Not played on its item page.
	entry := entryFor(t, e.s, e.alice.ID, "620")
	want(t, e.c.PostForm("/library/"+entry.String()+"/played", url.Values{"played": {"false"}}), http.StatusSeeOther)
	contains(t, text(e.c.Get(item).Body), "Not played (you) · Steam says played", "Reset to source")

	// 3. Sync again with more playtime and new achievements.
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 2400)}
	set.Unlocks = []sync.Unlock{{Key: "a1"}, {Key: "a2", UnlockedAt: &at}}
	e.fake.Sets = []sync.AchievementSet{set}
	if res := e.sync(t); res.Status != db.SyncRunStatusSucceeded {
		t.Fatalf("second sync: %s %v", res.Status, res.Warnings)
	}

	// 4. Still Not played; the source playtime and achievements advanced; history shows the respected override.
	page = text(e.c.Get(item).Body)
	contains(t, page, "Not played (you) · Steam says played", "Playtime 40 h", "Achievements 2 / 3")
	if strings.Contains(page, "Played · inferred from Steam") {
		t.Error("override lost: page shows inferred played")
	}
	history := e.c.Get(e.historyURL())
	want(t, history, http.StatusOK)
	runID := scalar[uuid.UUID](t, e.s.Pool, `select r.public_id from sync_runs r join sync_events ev on ev.run_id = r.id
		where ev.kind = 'override_respected' and ev.field = 'played' and ev.respected_override`)
	if !strings.Contains(history.Body, `data-run="`+runID.String()+`"`) {
		t.Fatal("history doesn't list the run with the respected override")
	}
	events := e.c.Form(http.MethodGet, e.historyURL()+"?run="+runID.String(), nil, true)
	want(t, events, http.StatusOK)
	if strings.Contains(events.Body, "<html") {
		t.Error("partial run request rendered a full page")
	}
	respected := regexp.MustCompile(`(?s)<li class="[^"]*bg-accent/10[^"]*" data-kind="override_respected">.*?</li>`).FindString(events.Body)
	contains(t, text(respected), "Portal 2", "Steam says played; you said not played", "Your choice was kept")
	full := text(e.c.Get(e.historyURL() + "?run=" + runID.String()).Body)
	contains(t, full, "Your choice was kept", "Playtime: 30 h → 40 h", "Unlocked achievement You Monster")

	// 5. Reset to source makes it Played.
	want(t, e.c.PostForm("/library/"+entry.String()+"/played", url.Values{"played": {"reset"}}), http.StatusSeeOther)
	page = text(e.c.Get(item).Body)
	contains(t, page, "Played · inferred from Steam")
	if strings.Contains(page, "Steam says") {
		t.Error("reset still shows an override")
	}
}

func TestExcludedAndDeletedGamesStayOut(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 60), game("400", "Portal", 30), game("70", "Half-Life", 10)}
	e.sync(t)

	// Exclude Portal from its item page: data stays, sync stops updating it.
	portal := itemFor(t, e.s, "400")
	page := e.c.Get("/items/" + portal.String()).Body
	contains(t, page, "Exclude from sync")
	res := e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/exclusions",
		url.Values{"key": {"400"}, "item": {portal.String()}, "next": {"/items/" + portal.String()}})
	want(t, res, http.StatusSeeOther)
	if loc := res.Header.Get("Location"); loc != "/items/"+portal.String() {
		t.Fatalf("redirect to %q", loc)
	}
	contains(t, text(e.c.Get("/items/"+portal.String()).Body), "Excluded from sync", "Include again")
	contains(t, e.c.Get(e.historyURL()).Body, "Excluded from sync", `data-exclusion="400"`, "Include again")

	// Delete Half-Life: the confirmation says it stays deleted, and the delete excludes it.
	hl := itemFor(t, e.s, "70")
	contains(t, e.c.Get("/items/"+hl.String()).Body, "excluded from future syncs and stay removed")
	want(t, e.c.PostForm("/library/"+entryFor(t, e.s, e.alice.ID, "70").String()+"/delete", nil), http.StatusSeeOther)
	if n := scalar[int](t, e.s.Pool, `select count(*) from import_exclusions where account_id = $1 and provider_item_key = '70'`, e.acct.ID); n != 1 {
		t.Fatalf("delete didn't exclude: %d", n)
	}

	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 90), game("400", "Portal", 300), game("70", "Half-Life", 500)}
	res2 := e.sync(t)
	if res2.Counts.Excluded != 2 || res2.Counts.EntriesCreated != 0 {
		t.Fatalf("counts %+v", res2.Counts)
	}
	if n := entryCount(t, e.s, e.alice.ID, "70"); n != 0 {
		t.Fatal("deleted game was re-imported")
	}
	if n := entryCount(t, e.s, e.alice.ID, "400"); n != 1 {
		t.Fatal("excluding removed the existing entry")
	}
	if m := scalar[int](t, e.s.Pool, `select minutes_total from external_playtime where provider_item_key = '400'`); m != 30 {
		t.Fatalf("excluded game's playtime updated to %d", m)
	}

	// Include Portal again (htmx DELETE); the next sync updates it.
	res = e.c.Form(http.MethodDelete, "/connections/"+e.acct.PublicID.String()+"/exclusions/400?next=/items/"+portal.String(), nil, true)
	want(t, res, http.StatusNoContent)
	if res.Header.Get("HX-Redirect") != "/items/"+portal.String() {
		t.Fatalf("HX-Redirect %q", res.Header.Get("HX-Redirect"))
	}
	e.sync(t)
	if m := scalar[int](t, e.s.Pool, `select minutes_total from external_playtime where provider_item_key = '400'`); m != 300 {
		t.Fatalf("included game's playtime = %d", m)
	}
	// The no-JS fallback removes an exclusion too.
	want(t, e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/exclusions/70/delete", nil), http.StatusSeeOther)
	if n := scalar[int](t, e.s.Pool, `select count(*) from import_exclusions where account_id = $1`, e.acct.ID); n != 0 {
		t.Fatalf("exclusions left: %d", n)
	}
	want(t, e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/exclusions/70/delete", nil), http.StatusNotFound)
	want(t, e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/exclusions", url.Values{"key": {""}}), http.StatusUnprocessableEntity)
}

func TestUnlink(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{game("1", "Untouched", 10), game("2", "Noted", 10), game("3", "Marked", 10), game("4", "Reviewed", 0)}
	e.sync(t)
	// A game the user added by hand before linking isn't an import.
	manual := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "Manual")
	testutil.CreateEntry(t, e.s.Pool, e.alice.ID, manual.ID)

	noted := entryFor(t, e.s, e.alice.ID, "2")
	want(t, e.c.PostForm("/library/"+noted.String(), url.Values{"visibility": {"private"}, "notes": {"keep me"}}), http.StatusSeeOther)
	want(t, e.c.PostForm("/library/"+entryFor(t, e.s, e.alice.ID, "3").String()+"/played", url.Values{"played": {"false"}}), http.StatusSeeOther)
	reviewed := scalar[int64](t, e.s.Pool, `select media_item_id from external_ids where value = '4'`)
	testutil.CreateReview(t, e.s.Pool, e.alice.ID, reviewed, 80, db.VisibilityPrivate)

	confirm := e.c.Get("/connections/" + e.acct.PublicID.String() + "/unlink")
	want(t, confirm, http.StatusOK)
	page := text(confirm.Body)
	contains(t, page, "Keep imported entries", "Remove imported entries", "added 4 entries", "deletes 1 you haven't changed", "Noted", "Marked", "Reviewed")
	if strings.Contains(page, "Untouched") {
		t.Error("untouched entry listed as kept")
	}

	res := e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/unlink", url.Values{"mode": {"remove"}})
	want(t, res, http.StatusSeeOther)
	if res.Header.Get("Location") != "/settings/connections" {
		t.Fatalf("redirect %q", res.Header.Get("Location"))
	}
	contains(t, e.c.Get("/settings/connections").Body, "Removed 1 imported entry; kept 3 you had changed.")
	for key, n := range map[string]int{"1": 0, "2": 1, "3": 1, "4": 1} {
		if got := entryCount(t, e.s, e.alice.ID, key); got != n {
			t.Errorf("game %s: %d entries, want %d", key, got, n)
		}
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from library_entries where media_item_id = $1`, manual.ID); n != 1 {
		t.Error("manual entry removed")
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from media_items where id in (select media_item_id from external_ids where provider = 'steam_app')`); n != 4 {
		t.Errorf("catalog items deleted: %d left", n)
	}
	for _, table := range []string{"connected_accounts", "sync_runs", "sync_events", "external_playtime"} {
		if n := scalar[int](t, e.s.Pool, `select count(*) from `+table); n != 0 {
			t.Errorf("%s has %d rows after unlink", table, n)
		}
	}
	want(t, e.c.Get(e.historyURL()), http.StatusNotFound)

	// Unlink with keep leaves every entry.
	acct2 := testutil.CreateConnectedAccount(t, e.s.Pool, e.alice.ID, "steam", "76561197960287931")
	e.fake.Games = []sync.OwnedGame{game("1", "Untouched", 10), game("5", "Another", 10)}
	syncAccount(t, e.s, acct2)
	before := scalar[int](t, e.s.Pool, `select count(*) from library_entries where user_id = $1`, e.alice.ID)
	want(t, e.c.PostForm("/connections/"+acct2.PublicID.String()+"/unlink", url.Values{"mode": {"keep"}}), http.StatusSeeOther)
	if after := scalar[int](t, e.s.Pool, `select count(*) from library_entries where user_id = $1`, e.alice.ID); after != before {
		t.Fatalf("keep removed entries: %d -> %d", before, after)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from connected_accounts`); n != 0 {
		t.Fatal("account not deleted")
	}
	want(t, e.c.PostForm("/connections/"+acct2.PublicID.String()+"/unlink", url.Values{"mode": {"keep"}}), http.StatusNotFound)
}

func TestUnlinkKeepsGamesAnotherAccountOwns(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 10)}
	e.sync(t)
	other := testutil.CreateConnectedAccount(t, e.s.Pool, e.alice.ID, "steam", "76561197960287931")
	syncAccount(t, e.s, other)
	want(t, e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/unlink", url.Values{"mode": {"remove"}}), http.StatusSeeOther)
	if n := entryCount(t, e.s, e.alice.ID, "620"); n != 1 {
		t.Fatal("removed an entry another linked account still owns")
	}
	want(t, e.c.PostForm("/connections/"+other.PublicID.String()+"/unlink", url.Values{"mode": {"maybe"}}), http.StatusUnprocessableEntity)
}

func TestHistoryPagesAndFilters(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 10), game("999", "Obscure Game", 5)}
	e.fake.Warnings = []string{"Game details are private"}
	e.sync(t)
	e.fake.Warnings = nil
	for range 21 {
		e.sync(t)
	}
	first := e.c.Get(e.historyURL())
	want(t, first, http.StatusOK)
	if n := strings.Count(first.Body, "data-run="); n != 20 {
		t.Fatalf("first page has %d runs", n)
	}
	older := regexp.MustCompile(`href="(/settings/connections/[^"]+\?before=[^"]+)"`).FindStringSubmatch(first.Body)
	if older == nil {
		t.Fatal("no Older link")
	}
	second := e.c.Get(older[1])
	want(t, second, http.StatusOK)
	if n := strings.Count(second.Body, "data-run="); n != 2 {
		t.Fatalf("second page has %d runs", n)
	}
	contains(t, text(second.Body), "Partial", "Game details are private", "2 games in library · 2 added · 2 new items", "Newest")

	review := text(e.c.Get(e.historyURL() + "?filter=needs_review").Body)
	contains(t, review, "Obscure Game", "Portal 2", "needs review")
	if strings.Contains(review, "Added to your library") {
		t.Error("needs_review filter shows other events")
	}
	want(t, e.c.Get(e.historyURL()+"?run=not-a-uuid"), http.StatusNotFound)
	want(t, e.c.Get(e.historyURL()+"?run="+uuid.New().String()), http.StatusNotFound)
}

func TestCrossUser(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 1800)}
	e.sync(t)
	want(t, e.c.PostForm("/connections/"+e.acct.PublicID.String()+"/exclusions", url.Values{"key": {"400"}}), http.StatusSeeOther)
	runID := scalar[uuid.UUID](t, e.s.Pool, `select public_id from sync_runs limit 1`)

	bob := testutil.CreateUser(t, e.s.Q, "bob")
	b := e.s.LoginAs(t, bob)
	base := "/connections/" + e.acct.PublicID.String()
	for name, res := range map[string]*testutil.Response{
		"history":        b.Get(e.historyURL()),
		"run":            b.Form(http.MethodGet, e.historyURL()+"?run="+runID.String(), nil, true),
		"exclude":        b.PostForm(base+"/exclusions", url.Values{"key": {"620"}}),
		"include":        b.Form(http.MethodDelete, base+"/exclusions/400", nil, false),
		"include-post":   b.PostForm(base+"/exclusions/400/delete", nil),
		"unlink-confirm": b.Get(base + "/unlink"),
		"unlink":         b.PostForm(base+"/unlink", url.Values{"mode": {"remove"}}),
	} {
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: bob got %d, want 404", name, res.StatusCode)
		}
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from import_exclusions where account_id = $1`, e.acct.ID); n != 1 {
		t.Fatalf("bob changed alice's exclusions: %d", n)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from connected_accounts where id = $1`, e.acct.ID); n != 1 {
		t.Fatal("bob unlinked alice's account")
	}
	if n := entryCount(t, e.s, e.alice.ID, "620"); n != 1 {
		t.Fatal("bob removed alice's entry")
	}

	// Bob owns the same game on his own account: his item page shows only his source.
	bobAcct := testutil.CreateConnectedAccount(t, e.s.Pool, bob.ID, "steam", "76561197960287999")
	e.fake.Games = []sync.OwnedGame{game("620", "Portal 2", 5)}
	syncAccount(t, e.s, bobAcct)
	page := text(b.Get("/items/" + itemFor(t, e.s, "620").String()).Body)
	contains(t, page, "Playtime 5 min")
	if strings.Contains(page, "30 h") || strings.Contains(page, e.acct.PublicID.String()) {
		t.Error("bob sees alice's synced source")
	}
	if strings.Contains(b.Get("/settings/connections/"+bobAcct.PublicID.String()+"/history").Body, e.acct.PublicID.String()) {
		t.Error("bob's history mentions alice's account")
	}
}
