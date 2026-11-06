package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

type env struct {
	s    *testutil.Server
	fake *synctest.Fake
	user db.User
	acct db.ConnectedAccount
}

// guardSQL makes any sync write to user-owned data fail loudly (ADR 0002). Tests
// simulate user actions through userAction, which sets openshelf.user_action.
const guardSQL = `
create function test_guard_entries() returns trigger language plpgsql as $$
begin
  if coalesce(current_setting('openshelf.user_action', true), '') = 'on' then
    return coalesce(new, old);
  end if;
  if tg_op = 'DELETE' then
    raise exception 'sync deleted a library entry';
  end if;
  if tg_op = 'UPDATE' and (
       new.ownership_user is distinct from old.ownership_user or new.played_user is distinct from old.played_user
    or new.backlog is distinct from old.backlog or new.visibility is distinct from old.visibility
    or new.notes is distinct from old.notes or new.tags is distinct from old.tags
    or new.acquired_on is distinct from old.acquired_on or new.edition_id is distinct from old.edition_id
    or new.user_id is distinct from old.user_id or new.media_item_id is distinct from old.media_item_id) then
    raise exception 'sync wrote a user-owned library_entries column';
  end if;
  if tg_op = 'INSERT' and (new.ownership_user is not null or new.played_user is not null or new.backlog
    or new.visibility <> 'private' or new.notes is not null) then
    raise exception 'sync created an entry with user-owned values';
  end if;
  return coalesce(new, old);
end $$;
create trigger test_guard_entries before insert or update or delete on library_entries
  for each row execute function test_guard_entries();

create function test_guard_forbid() returns trigger language plpgsql as $$
begin
  if coalesce(current_setting('openshelf.user_action', true), '') = 'on' then
    return null;
  end if;
  raise exception 'sync wrote %', tg_table_name;
end $$;
create trigger test_guard_forbid before insert or update or delete on reviews for each statement execute function test_guard_forbid();
create trigger test_guard_forbid before insert or update or delete on experiences for each statement execute function test_guard_forbid();
create trigger test_guard_forbid before insert or update or delete on lists for each statement execute function test_guard_forbid();
create trigger test_guard_forbid before insert or update or delete on list_items for each statement execute function test_guard_forbid();
create trigger test_guard_forbid before insert or update or delete on activity_events for each statement execute function test_guard_forbid();
`

func setup(t *testing.T) *env {
	t.Helper()
	s := testutil.NewServer(t)
	if _, err := s.Pool.Exec(context.Background(), guardSQL); err != nil {
		t.Fatal(err)
	}
	f := &synctest.Fake{}
	s.Deps.SyncAdapters = sync.NewRegistry(f)
	u := testutil.CreateUser(t, s.Q, "alice")
	acct := testutil.CreateConnectedAccount(t, s.Pool, u.ID, "fake", "76561197960287930")
	return &env{s: s, fake: f, user: u, acct: acct}
}

func userAction(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "select set_config('openshelf.user_action', 'on', true)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (e *env) run(t *testing.T) runner.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), e.s.Deps, e.acct.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func ip(v int) *int { return &v }

func steamGame(key, title string, minutes *int) sync.OwnedGame {
	return sync.OwnedGame{
		Key: key, Title: title, PlaytimeMinutes: minutes,
		ExternalIDs: []providers.ExternalID{{Provider: "steam_app", Value: key}},
	}
}

func itemFor(t *testing.T, e *env, key string) int64 {
	t.Helper()
	return scalar[int64](t, e.s.Pool, `select media_item_id from external_ids where provider = 'steam_app' and value = $1`, key)
}

type entryRow struct {
	OwnershipUser, OwnershipSource *string
	PlayedUser, PlayedSource       *bool
	Backlog                        bool
	Visibility                     string
	Effective                      *bool
}

func entry(t *testing.T, e *env, userID, itemID int64) entryRow {
	t.Helper()
	var r entryRow
	err := e.s.Pool.QueryRow(context.Background(), `
		select ownership_user::text, ownership_source::text, played_user, played_source, backlog, visibility::text,
		       coalesce(played_user, played_source)
		from library_entries where user_id = $1 and media_item_id = $2`, userID, itemID).
		Scan(&r.OwnershipUser, &r.OwnershipSource, &r.PlayedUser, &r.PlayedSource, &r.Backlog, &r.Visibility, &r.Effective)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	return r
}

func countEvents(t *testing.T, e *env, kind string) int {
	t.Helper()
	return scalar[int](t, e.s.Pool, `select count(*) from sync_events where account_id = $1 and kind = $2`, e.acct.ID, kind)
}

func TestFirstImport(t *testing.T) {
	e := setup(t)
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	tf2 := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "Team Fortress 2")
	if _, err := e.s.Pool.Exec(context.Background(),
		`insert into external_ids (provider, value, media_item_id) values ('steam_app', '440', $1)`, tf2.ID); err != nil {
		t.Fatal(err)
	}
	bobEntry := testutil.CreateEntry(t, e.s.Pool, bob.ID, tf2.ID)

	portal := steamGame("620", "Portal 2", ip(1800))
	portal.CoverURL = "https://cdn.example/620.jpg"
	last := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	portal.LastPlayedAt = &last
	e.fake.Games = []sync.OwnedGame{portal, steamGame("440", "Team Fortress 2", ip(0)), steamGame("999", "Unplayed", ip(0))}
	e.fake.Cursor = []byte(`{"v":1}`)

	res := e.run(t)
	if res.Status != db.SyncRunStatusSucceeded || res.Err != nil {
		t.Fatalf("status %s err %v warnings %v", res.Status, res.Err, res.Warnings)
	}
	if c := res.Counts; c.Games != 3 || c.ItemsCreated != 2 || c.EntriesCreated != 3 {
		t.Fatalf("counts %+v", c)
	}
	if got := itemFor(t, e, "440"); got != tf2.ID {
		t.Fatalf("440 matched item %d, want existing %d", got, tf2.ID)
	}

	p := entry(t, e, e.user.ID, itemFor(t, e, "620"))
	if p.OwnershipUser != nil || p.PlayedUser != nil || p.Backlog || p.Visibility != "private" {
		t.Fatalf("new entry has user values: %+v", p)
	}
	if p.OwnershipSource == nil || *p.OwnershipSource != "owned" || p.PlayedSource == nil || !*p.PlayedSource {
		t.Fatalf("portal sources: %+v", p)
	}
	if u := entry(t, e, e.user.ID, itemFor(t, e, "999")); u.PlayedSource != nil {
		t.Fatalf("zero playtime must not set played_source: %+v", u)
	}

	var minutes int
	var lp *time.Time
	if err := e.s.Pool.QueryRow(context.Background(),
		`select minutes_total, last_played_at from external_playtime where account_id = $1 and provider_item_key = '620'`, e.acct.ID).
		Scan(&minutes, &lp); err != nil {
		t.Fatal(err)
	}
	if minutes != 1800 || lp == nil || !lp.Equal(last) {
		t.Fatalf("playtime %d %v", minutes, lp)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from media_items m join external_ids x on x.media_item_id = m.id
		where x.value = '620' and m.cover_image_id is not null`); n != 1 {
		t.Fatal("manual item should get the cover")
	}

	if n := countEvents(t, e, sync.EventEntryCreated); n != 3 {
		t.Fatalf("entry_created events = %d", n)
	}
	if n := countEvents(t, e, sync.EventNeedsReview); n != 2 {
		t.Fatalf("needs_review events = %d", n)
	}
	if n := countEvents(t, e, sync.EventPlaytimeChanged); n != 1 {
		t.Fatalf("playtime_changed events = %d", n)
	}

	var status string
	var cursor []byte
	var lastSynced, next *time.Time
	if err := e.s.Pool.QueryRow(context.Background(),
		`select status::text, cursor, last_synced_at, next_sync_at from connected_accounts where id = $1`, e.acct.ID).
		Scan(&status, &cursor, &lastSynced, &next); err != nil {
		t.Fatal(err)
	}
	if status != "active" || string(cursor) != `{"v":1}` || lastSynced == nil {
		t.Fatalf("account %s %q %v", status, cursor, lastSynced)
	}
	if d := time.Until(*next); d < 10*time.Hour || d > 14*time.Hour {
		t.Fatalf("next sync in %v", d)
	}
	if s := scalar[string](t, e.s.Pool, `select status::text from sync_runs where id = $1`, res.RunID); s != "succeeded" {
		t.Fatalf("run status %s", s)
	}

	if n := scalar[int](t, e.s.Pool, `select count(*) from library_entries where user_id = $1`, bob.ID); n != 1 {
		t.Fatalf("bob has %d entries", n)
	}
	if b := entry(t, e, bob.ID, tf2.ID); b.OwnershipSource != nil {
		t.Fatalf("bob's entry %d was touched: %+v", bobEntry.ID, b)
	}
}

func TestResyncIsIdempotent(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(1800)), steamGame("400", "Portal", ip(0))}
	e.fake.Sets = []sync.AchievementSet{{
		Key:          "620",
		Achievements: []sync.Achievement{{Key: "a1", Name: "Wake Up Call"}, {Key: "a2", Name: "You Monster"}},
		Unlocks:      []sync.Unlock{{Key: "a1"}},
	}}
	e.fake.Cursor = []byte("c1")
	e.run(t)
	counts := func() [5]int {
		return [5]int{
			scalar[int](t, e.s.Pool, `select count(*) from media_items`),
			scalar[int](t, e.s.Pool, `select count(*) from library_entries`),
			scalar[int](t, e.s.Pool, `select count(*) from sync_events`),
			scalar[int](t, e.s.Pool, `select count(*) from achievements`),
			scalar[int](t, e.s.Pool, `select count(*) from achievement_unlocks`),
		}
	}
	before := counts()
	res := e.run(t)
	if string(e.fake.LastCursor) != "c1" {
		t.Fatalf("second run got cursor %q", e.fake.LastCursor)
	}
	if after := counts(); after != before {
		t.Fatalf("re-sync changed rows: %v -> %v", before, after)
	}
	if res.Counts.EntriesCreated != 0 || res.Counts.EntriesUpdated != 0 || res.Counts.ItemsCreated != 0 || res.Counts.Unlocks != 0 {
		t.Fatalf("counts %+v", res.Counts)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from sync_runs where account_id = $1`, e.acct.ID); n != 2 {
		t.Fatalf("runs = %d", n)
	}
}

// The README's Portal 2 acceptance test at the runner level.
func TestOverrideRespected(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(1800))}
	e.fake.Sets = []sync.AchievementSet{{
		Key:          "620",
		Achievements: []sync.Achievement{{Key: "a1", Name: "Wake Up Call"}, {Key: "a2", Name: "You Monster"}},
		Unlocks:      []sync.Unlock{{Key: "a1"}},
	}}
	e.run(t)
	portal := itemFor(t, e, "620")
	userAction(t, e.s.Pool, `update library_entries set played_user = false where user_id = $1 and media_item_id = $2`, e.user.ID, portal)

	// A game the user already had, marked not played and not owned, that Steam reports as played.
	hl := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "Half-Life")
	if _, err := e.s.Pool.Exec(context.Background(), `insert into external_ids (provider, value, media_item_id) values ('steam_app', '70', $1)`, hl.ID); err != nil {
		t.Fatal(err)
	}
	testutil.CreateEntry(t, e.s.Pool, e.user.ID, hl.ID)
	userAction(t, e.s.Pool, `update library_entries set played_user = false, ownership_user = 'not_owned' where media_item_id = $1`, hl.ID)

	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(2400)), steamGame("70", "Half-Life", ip(60))}
	e.fake.Sets[0].Unlocks = []sync.Unlock{{Key: "a1"}, {Key: "a2", UnlockedAt: &at}}
	res := e.run(t)
	if res.Status != db.SyncRunStatusSucceeded {
		t.Fatalf("status %s: %v %v", res.Status, res.Err, res.Warnings)
	}

	p := entry(t, e, e.user.ID, portal)
	if p.PlayedUser == nil || *p.PlayedUser || p.Effective == nil || *p.Effective {
		t.Fatalf("personal status must stay Not Played: %+v", p)
	}
	if m := scalar[int](t, e.s.Pool, `select minutes_total from external_playtime where provider_item_key = '620'`); m != 2400 {
		t.Fatalf("playtime = %d", m)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from achievement_unlocks where account_id = $1`, e.acct.ID); n != 2 {
		t.Fatalf("unlocks = %d", n)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from sync_events where media_item_id = $1 and kind = $2
		and field = 'played' and respected_override and before = 'true' and after = 'false'`, portal, sync.EventOverrideRespected); n != 1 {
		t.Fatalf("override_respected events for portal = %d", n)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from sync_events where media_item_id = $1 and kind = $2 and field = 'a2'`,
		portal, sync.EventAchievementUnlocked); n != 1 {
		t.Fatalf("achievement_unlocked events = %d", n)
	}

	h := entry(t, e, e.user.ID, hl.ID)
	if *h.PlayedUser || !*h.PlayedSource || *h.OwnershipUser != "not_owned" || *h.OwnershipSource != "owned" {
		t.Fatalf("half-life: %+v", h)
	}
	for _, kind := range []string{sync.EventPlayedSourceChanged, sync.EventOwnershipSourceChanged} {
		if n := scalar[int](t, e.s.Pool, `select count(*) from sync_events where media_item_id = $1 and kind = $2 and respected_override`,
			hl.ID, kind); n != 1 {
			t.Fatalf("%s with respected_override = %d", kind, n)
		}
	}
}

func TestExclusionSkipped(t *testing.T) {
	e := setup(t)
	if _, err := e.s.Pool.Exec(context.Background(),
		`insert into import_exclusions (account_id, provider_item_key) values ($1, '620')`, e.acct.ID); err != nil {
		t.Fatal(err)
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(1800)), steamGame("400", "Portal", ip(5))}
	e.fake.Sets = []sync.AchievementSet{{Key: "620", Achievements: []sync.Achievement{{Key: "a1", Name: "x"}}, Unlocks: []sync.Unlock{{Key: "a1"}}}}
	res := e.run(t)
	if res.Counts.Excluded != 1 || res.Counts.EntriesCreated != 1 {
		t.Fatalf("counts %+v", res.Counts)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from external_ids where provider = 'steam_app' and value = '620'`); n != 0 {
		t.Fatal("excluded game was imported")
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from external_playtime where provider_item_key = '620'`); n != 0 {
		t.Fatal("excluded playtime stored")
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from achievements`); n != 0 {
		t.Fatal("excluded achievements stored")
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from sync_events where kind = $1 and provider_item_key = '620'`, sync.EventExcludedSkipped); n != 1 {
		t.Fatalf("excluded_skipped events = %d", n)
	}
}

func TestDisappearanceKeepsData(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(1800)), steamGame("400", "Portal", ip(30))}
	e.fake.Sets = []sync.AchievementSet{{Key: "400", Achievements: []sync.Achievement{{Key: "a1", Name: "x"}}, Unlocks: []sync.Unlock{{Key: "a1"}}}}
	e.run(t)

	// Portal vanished; Portal 2 came back without playtime (e.g. hidden game details).
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", nil)}
	e.fake.Sets = nil
	e.run(t)

	if n := scalar[int](t, e.s.Pool, `select count(*) from library_entries where user_id = $1`, e.user.ID); n != 2 {
		t.Fatalf("entries = %d", n)
	}
	if p := entry(t, e, e.user.ID, itemFor(t, e, "620")); p.PlayedSource == nil || !*p.PlayedSource {
		t.Fatalf("missing playtime cleared played_source: %+v", p)
	}
	if m := scalar[int](t, e.s.Pool, `select minutes_total from external_playtime where provider_item_key = '620'`); m != 1800 {
		t.Fatalf("missing playtime cleared minutes: %d", m)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from external_playtime where provider_item_key = '400'`); n != 1 {
		t.Fatal("vanished game's playtime deleted")
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from achievement_unlocks`); n != 1 {
		t.Fatal("vanished game's unlocks deleted")
	}
}

func TestPartialWarningRecorded(t *testing.T) {
	e := setup(t)
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(10))}
	e.fake.Warnings = []string{"Game details are private; playtime unavailable"}
	res := e.run(t)
	if res.Status != db.SyncRunStatusPartial {
		t.Fatalf("status %s", res.Status)
	}
	var warnings []string
	var status string
	if err := e.s.Pool.QueryRow(context.Background(), `select warnings, status::text from sync_runs where id = $1`, res.RunID).
		Scan(&warnings, &status); err != nil {
		t.Fatal(err)
	}
	if status != "partial" || len(warnings) != 1 || warnings[0] != e.fake.Warnings[0] {
		t.Fatalf("run %s %v", status, warnings)
	}
	if s := scalar[string](t, e.s.Pool, `select status::text from connected_accounts where id = $1`, e.acct.ID); s != "active" {
		t.Fatalf("account status %s", s)
	}
}

func TestAdapterErrorKeepsCursor(t *testing.T) {
	e := setup(t)
	if _, err := e.s.Pool.Exec(context.Background(), `update connected_accounts set cursor = 'old' where id = $1`, e.acct.ID); err != nil {
		t.Fatal(err)
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(10))}
	e.fake.Cursor = []byte("new")
	e.fake.Err = errors.New("steam: 503")
	res := e.run(t)
	if res.Status != db.SyncRunStatusPartial || res.Err == nil {
		t.Fatalf("status %s err %v", res.Status, res.Err)
	}
	var status, lastErr string
	var cursor []byte
	var next time.Time
	if err := e.s.Pool.QueryRow(context.Background(),
		`select status::text, last_error, cursor, next_sync_at from connected_accounts where id = $1`, e.acct.ID).
		Scan(&status, &lastErr, &cursor, &next); err != nil {
		t.Fatal(err)
	}
	if status != "error" || lastErr != "steam: 503" || string(cursor) != "old" {
		t.Fatalf("account %s %q %q", status, lastErr, cursor)
	}
	if d := time.Until(next); d > 2*time.Hour {
		t.Fatalf("retry in %v", d)
	}

	e.fake.Games = nil
	if res := e.run(t); res.Status != db.SyncRunStatusFailed {
		t.Fatalf("no progress + error should fail, got %s", res.Status)
	}
	e.fake.Err = nil
	e.run(t)
	if s := scalar[string](t, e.s.Pool, `select status::text from connected_accounts where id = $1`, e.acct.ID); s != "active" {
		t.Fatalf("recovered account status %s", s)
	}
}

func TestUnconfiguredAdapterFails(t *testing.T) {
	e := setup(t)
	e.fake.Unconfigured = true
	res := e.run(t)
	if res.Status != db.SyncRunStatusFailed || !errors.Is(res.Err, runner.ErrAdapterUnavailable) || e.fake.Calls != 0 {
		t.Fatalf("status %s err %v calls %d", res.Status, res.Err, e.fake.Calls)
	}
}

func TestDisabledAccountSkipped(t *testing.T) {
	e := setup(t)
	if _, err := e.s.Pool.Exec(context.Background(), `update connected_accounts set status = 'disabled' where id = $1`, e.acct.ID); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	if e.fake.Calls != 0 {
		t.Fatal("disabled account was synced")
	}
}

func TestResolver(t *testing.T) {
	e := setup(t)
	e.fake.ResolveFunc = func(ctx context.Context, g sync.OwnedGame) (*sync.Resolution, error) {
		switch g.Key {
		case "620":
			return &sync.Resolution{Provider: "igdb", Item: &providers.Item{
				Kind: db.MediaKindGame, Title: "Portal 2", ReleaseYear: 2011,
				ExternalIDs: []providers.ExternalID{{Provider: "igdb_game", Value: "72"}},
			}}, nil
		case "500":
			return nil, errors.New("igdb timeout")
		}
		return nil, nil
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(10)), steamGame("500", "Left 4 Dead", ip(10))}
	res := e.run(t)
	if res.Counts.ItemsResolved != 1 || res.Counts.Skipped != 1 || res.Counts.ItemsCreated != 0 {
		t.Fatalf("counts %+v", res.Counts)
	}
	if res.Status != db.SyncRunStatusPartial || len(res.Warnings) != 1 {
		t.Fatalf("status %s warnings %v", res.Status, res.Warnings)
	}
	id := itemFor(t, e, "620")
	if p := scalar[string](t, e.s.Pool, `select metadata_provider from media_items where id = $1`, id); p != "igdb" {
		t.Fatalf("metadata_provider %q", p)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from external_ids where media_item_id = $1 and provider = 'igdb_game'`, id); n != 1 {
		t.Fatal("igdb id missing")
	}
	if n := countEvents(t, e, sync.EventNeedsReview); n != 0 {
		t.Fatalf("resolved items don't need review, got %d", n)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from external_ids where value = '500'`); n != 0 {
		t.Fatal("a resolver error must not create a manual item")
	}
}

func TestAchievementsRefresh(t *testing.T) {
	e := setup(t)
	set := sync.AchievementSet{
		Key: "620",
		Achievements: []sync.Achievement{
			{Key: "a1", Name: "One", Order: 1}, {Key: "a2", Name: "Two", Order: 2}, {Key: "a3", Name: "Three", Hidden: true, Order: 3},
		},
		Unlocks: []sync.Unlock{{Key: "a1"}, {Key: "a2"}},
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(10))}
	e.fake.Sets = []sync.AchievementSet{set}
	e.run(t)

	// Next run: schema renamed, a3 unlocked, and the game itself isn't re-emitted.
	e.fake.Games = nil
	set.Achievements[0].Name = "One!"
	set.Unlocks = append(set.Unlocks, sync.Unlock{Key: "a3"}, sync.Unlock{Key: "missing"})
	e.fake.Sets = []sync.AchievementSet{set}
	res := e.run(t)
	if res.Counts.Unlocks != 1 || len(res.Warnings) != 1 {
		t.Fatalf("counts %+v warnings %v", res.Counts, res.Warnings)
	}
	item := itemFor(t, e, "620")
	if n := scalar[int](t, e.s.Pool, `select count(*) from achievements where media_item_id = $1`, item); n != 3 {
		t.Fatalf("achievements = %d", n)
	}
	if name := scalar[string](t, e.s.Pool, `select name from achievements where provider_achievement_key = 'a1'`); name != "One!" {
		t.Fatalf("name %q", name)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from achievement_unlocks where account_id = $1 and mode = 'standard'`, e.acct.ID); n != 3 {
		t.Fatalf("unlocks = %d", n)
	}
	if n := countEvents(t, e, sync.EventAchievementUnlocked); n != 3 {
		t.Fatalf("unlock events = %d", n)
	}
}

func TestGuardTriggerCatchesUserWrites(t *testing.T) {
	e := setup(t)
	item := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "Portal 2")
	testutil.CreateEntry(t, e.s.Pool, e.user.ID, item.ID)
	for _, sql := range []string{
		`update library_entries set played_user = false`,
		`update library_entries set backlog = true`,
		`update library_entries set visibility = 'public'`,
		`delete from library_entries`,
		`insert into reviews (user_id, media_item_id, rating) values (1, 1, 50)`,
	} {
		if _, err := e.s.Pool.Exec(context.Background(), sql); err == nil {
			t.Errorf("guard allowed %q", sql)
		}
	}
}
