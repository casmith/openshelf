package steam_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/sync/steam"
	"github.com/casmith/openshelf/internal/testutil"
)

type importEnv struct {
	s     *testutil.Server
	steam *steam.FakeSteam
	igdb  *steam.FakeIGDB
	user  db.User
	acct  db.ConnectedAccount
}

func newImportEnv(t *testing.T) *importEnv {
	t.Helper()
	s := testutil.NewServer(t)
	fs := steam.NewFakeSteam(t)
	fi, ip := steam.NewFakeIGDB(t)
	s.Deps.SyncAdapters = sync.NewRegistry(fs.Adapter(providers.NewRegistry(ip)))
	u := testutil.CreateUser(t, s.Q, "alice")
	acct := testutil.CreateConnectedAccount(t, s.Pool, u.ID, "steam", aliceSteamID)
	return &importEnv{s: s, steam: fs, igdb: fi, user: u, acct: acct}
}

func (e *importEnv) run(t *testing.T) runner.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), e.s.Deps, e.acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func scalar[T any](t *testing.T, s *testutil.Server, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *importEnv) itemFor(t *testing.T, appID string) int64 {
	t.Helper()
	return scalar[int64](t, e.s, `select media_item_id from external_ids where provider = 'steam_app' and value = $1`, appID)
}

type entryRow struct {
	Ownership  *string
	Played     *bool
	Backlog    bool
	Visibility string
}

func (e *importEnv) entry(t *testing.T, itemID int64) entryRow {
	t.Helper()
	var r entryRow
	err := e.s.Pool.QueryRow(context.Background(),
		`select ownership_source::text, played_source, backlog, visibility::text from library_entries
		 where user_id = $1 and media_item_id = $2 and ownership_user is null and played_user is null`, e.user.ID, itemID).
		Scan(&r.Ownership, &r.Played, &r.Backlog, &r.Visibility)
	if err != nil {
		t.Fatalf("entry for item %d: %v", itemID, err)
	}
	return r
}

func TestImportOwnedGames(t *testing.T) {
	e := newImportEnv(t)
	portal := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "Portal")
	if _, err := e.s.Pool.Exec(context.Background(),
		`insert into external_ids (provider, value, media_item_id) values ('igdb_game', '71', $1)`, portal.ID); err != nil {
		t.Fatal(err)
	}

	res := e.run(t)
	if res.Err != nil || res.Status != db.SyncRunStatusSucceeded || len(res.Warnings) != 0 {
		t.Fatalf("run: %+v", res)
	}
	if c := res.Counts; c.Games != 3 || c.ItemsResolved != 1 || c.ItemsCreated != 1 || c.EntriesCreated != 3 {
		t.Fatalf("counts %+v", c)
	}

	// IGDB match: the IGDB item, imported with its metadata.
	p2 := e.itemFor(t, "620")
	if title := scalar[string](t, e.s, `select title from media_items where id = $1`, p2); title != "Portal 2" {
		t.Errorf("portal 2 title %q", title)
	}
	if n := scalar[int](t, e.s, `select count(*) from external_ids where media_item_id = $1 and provider = 'igdb_game' and value = '72'`, p2); n != 1 {
		t.Error("portal 2 isn't the IGDB item")
	}
	// An existing IGDB item matches directly through the igdb_game hint.
	if got := e.itemFor(t, "400"); got != portal.ID {
		t.Errorf("portal matched item %d, want existing %d", got, portal.ID)
	}
	// No match: a manual item flagged for review.
	indie := e.itemFor(t, "2099990")
	if n := scalar[int](t, e.s, `select count(*) from external_ids where media_item_id = $1 and provider = 'igdb_game'`, indie); n != 0 {
		t.Error("unmatched game got an IGDB ID")
	}
	if n := scalar[int](t, e.s, `select count(*) from sync_events where kind = 'needs_review' and media_item_id = $1`, indie); n != 1 {
		t.Errorf("%d needs_review events for the manual item", n)
	}
	if n := scalar[int](t, e.s, `select count(*) from sync_events where kind = 'needs_review'`); n != 1 {
		t.Errorf("%d needs_review events, want only the manual item", n)
	}

	for appID, wantPlayed := range map[string]*bool{"620": ptr(true), "400": nil, "2099990": ptr(true)} {
		r := e.entry(t, e.itemFor(t, appID))
		if r.Ownership == nil || *r.Ownership != "owned" || r.Backlog || r.Visibility != "private" ||
			(wantPlayed == nil) != (r.Played == nil) || (wantPlayed != nil && *r.Played != *wantPlayed) {
			t.Errorf("%s entry %+v", appID, r)
		}
	}

	var minutes, twoWeeks int
	var lastPlayed int64
	if err := e.s.Pool.QueryRow(context.Background(),
		`select minutes_total, minutes_2weeks, extract(epoch from last_played_at)::bigint from external_playtime
		 where account_id = $1 and provider_item_key = '620'`, e.acct.ID).Scan(&minutes, &twoWeeks, &lastPlayed); err != nil {
		t.Fatal(err)
	}
	if minutes != 1800 || twoWeeks != 95 || lastPlayed != 1762300800 {
		t.Errorf("playtime %d/%d last %d", minutes, twoWeeks, lastPlayed)
	}
	if n := scalar[int](t, e.s, `select count(*) from external_playtime where account_id = $1`, e.acct.ID); n != 3 {
		t.Errorf("%d playtime rows", n)
	}

	// Re-running changes nothing.
	events := scalar[int](t, e.s, `select count(*) from sync_events`)
	entries := scalar[int](t, e.s, `select count(*) from library_entries`)
	items := scalar[int](t, e.s, `select count(*) from media_items`)
	res = e.run(t)
	if res.Status != db.SyncRunStatusSucceeded || res.Counts.EntriesCreated != 0 || res.Counts.EntriesUpdated != 0 ||
		res.Counts.ItemsCreated != 0 || res.Counts.ItemsResolved != 0 {
		t.Fatalf("second run %+v", res)
	}
	if scalar[int](t, e.s, `select count(*) from sync_events`) != events ||
		scalar[int](t, e.s, `select count(*) from library_entries`) != entries ||
		scalar[int](t, e.s, `select count(*) from media_items`) != items {
		t.Fatal("re-sync changed data")
	}
	if n := e.igdb.Count("/v4/games"); n != 1 {
		t.Errorf("%d IGDB fetches, want 1 (the second run matches by external ID)", n)
	}
}

func TestImportPrivateProfile(t *testing.T) {
	e := newImportEnv(t)
	p := e.steam.Players[aliceSteamID]
	p.CommunityVisibilityState = 1
	e.steam.Players[aliceSteamID] = p

	res := e.run(t)
	if res.Status != db.SyncRunStatusPartial || res.Err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "private") {
		t.Fatalf("run %+v", res)
	}
	if n := scalar[int](t, e.s, `select count(*) from library_entries`); n != 0 {
		t.Fatalf("%d entries from a private profile", n)
	}
	if e.steam.Count("/IPlayerService/GetOwnedGames/v1/") != 0 {
		t.Error("fetched games for a private profile")
	}
	if st := scalar[string](t, e.s, `select status::text from connected_accounts where id = $1`, e.acct.ID); st != "active" {
		t.Errorf("account status %s", st)
	}

	p.CommunityVisibilityState = 3
	e.steam.Players[aliceSteamID] = p
	e.steam.Owned[aliceSteamID] = `{"response":{}}`
	res = e.run(t)
	if res.Status != db.SyncRunStatusPartial || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Game details") {
		t.Fatalf("private game details: %+v", res)
	}
	if n := scalar[int](t, e.s, `select count(*) from library_entries`); n != 0 {
		t.Fatalf("%d entries from private game details", n)
	}
}

func TestImportForbiddenMarksAccountError(t *testing.T) {
	e := newImportEnv(t)
	e.steam.Key = "rotated"

	res := e.run(t)
	if res.Status != db.SyncRunStatusFailed || res.Err == nil {
		t.Fatalf("run %+v", res)
	}
	var status, lastError string
	if err := e.s.Pool.QueryRow(context.Background(),
		`select status::text, coalesce(last_error, '') from connected_accounts where id = $1`, e.acct.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "error" || !strings.Contains(lastError, "403") || !strings.Contains(lastError, "OPENSHELF_STEAM_API_KEY") {
		t.Fatalf("account %s: %q", status, lastError)
	}
	if body := e.s.LoginAs(t, e.user).Get("/settings/connections").Body; !strings.Contains(body, "OPENSHELF_STEAM_API_KEY") {
		t.Error("connections page doesn't show the error")
	}
}

func TestImportWithoutIGDBCreatesReviewItems(t *testing.T) {
	e := newImportEnv(t)
	e.s.Deps.SyncAdapters = sync.NewRegistry(e.steam.Adapter(nil))
	res := e.run(t)
	if res.Status != db.SyncRunStatusSucceeded || res.Counts.ItemsCreated != 3 {
		t.Fatalf("run %+v", res)
	}
	if n := scalar[int](t, e.s, `select count(*) from sync_events where kind = 'needs_review'`); n != 3 {
		t.Errorf("%d needs_review events", n)
	}
	if e.igdb.Count("/v4/external_games") != 0 {
		t.Error("looked up IGDB without it configured")
	}
}

func TestCrossUserIsolation(t *testing.T) {
	e := newImportEnv(t)
	if res := e.run(t); res.Status != db.SyncRunStatusSucceeded {
		t.Fatalf("run %+v", res)
	}
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	bc := e.s.LoginAs(t, bob)

	body := bc.Get("/settings/connections").Body
	if strings.Contains(body, e.acct.PublicID.String()) || strings.Contains(body, aliceSteamID) {
		t.Error("bob sees alice's Steam account")
	}
	jobs := scalar[int](t, e.s, `select count(*) from river_job where kind = 'sync_account'`)
	if res := bc.PostForm("/settings/connections/"+e.acct.PublicID.String()+"/sync", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("bob syncing alice's account: status %d", res.StatusCode)
	}
	if scalar[int](t, e.s, `select count(*) from river_job where kind = 'sync_account'`) != jobs {
		t.Error("bob enqueued a sync for alice's account")
	}
	if n := scalar[int](t, e.s, `select count(*) from library_entries where user_id = $1`, bob.ID); n != 0 {
		t.Errorf("bob got %d imported entries", n)
	}
	if res := bc.Get("/items/" + scalar[string](t, e.s, `select public_id::text from media_items where id = $1`, e.itemFor(t, "620"))); res.StatusCode == http.StatusOK &&
		strings.Contains(res.Body, aliceSteamID) {
		t.Error("item page leaks alice's Steam account to bob")
	}
}

func ptr[T any](v T) *T { return &v }
