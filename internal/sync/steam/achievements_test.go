package steam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	stdsync "sync"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/sync/steam"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

const steamID = "76561197960287930"

// fixture is a fake Steam Web API serving testdata/achievements_* and counting requests.
type fixture struct {
	t        *testing.T
	mu       stdsync.Mutex
	schema   map[string]string // appid -> testdata file; others get a one-achievement schema
	player   map[string]string // appid -> testdata file; others unlock that achievement
	status   map[string]int    // appid -> status for GetPlayerAchievements
	requests map[string]int    // "Method/appid" -> count
}

func newFixture(t *testing.T) (*fixture, *steam.Adapter) {
	f := &fixture{
		t:        t,
		schema:   map[string]string{"620": "achievements_schema_620.json", "70": "achievements_schema_empty.json"},
		player:   map[string]string{"620": "achievements_player_620.json"},
		status:   map[string]int{},
		requests: map[string]int{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := steam.New(config.Config{SteamAPIKey: "k"})
	a.Client.BaseURL = srv.URL
	a.Client.HTTP.Limiter = nil
	a.Client.HTTP.MaxRetries = 0
	return f, a
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	app := q.Get("appid")
	if q.Get("key") != "k" {
		http.Error(w, "bad key", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/ISteamUserStats/GetSchemaForGame/v2/":
		f.requests["schema/"+app]++
		if q.Get("l") != "english" {
			http.Error(w, "missing l", http.StatusBadRequest)
			return
		}
		if file, ok := f.schema[app]; ok {
			f.serveFile(w, file)
			return
		}
		_, _ = w.Write([]byte(`{"game":{"availableGameStats":{"achievements":[{"name":"A1","displayName":"First","hidden":0,"icon":"https://x/a1.jpg"}]}}}`))
	case "/ISteamUserStats/GetPlayerAchievements/v1/":
		f.requests["player/"+app]++
		if q.Get("steamid") != steamID {
			http.Error(w, "bad steamid", http.StatusBadRequest)
			return
		}
		if s := f.status[app]; s != 0 {
			w.WriteHeader(s)
			f.serveFile(w, "achievements_private.json")
			return
		}
		if file, ok := f.player[app]; ok {
			f.serveFile(w, file)
			return
		}
		_, _ = w.Write([]byte(`{"playerstats":{"achievements":[{"apiname":"A1","achieved":1,"unlocktime":0}],"success":true}}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) serveFile(w http.ResponseWriter, name string) {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
	}
	_, _ = w.Write(b)
}

func (f *fixture) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[key]
}

func (f *fixture) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.requests {
		n += v
	}
	return n
}

func (f *fixture) set(fn func(f *fixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

var acct = db.ConnectedAccount{ID: 1, Provider: steam.Provider, ExternalAccountID: steamID}

func portal(lastPlayed int64) steam.OwnedApp {
	return steam.OwnedApp{AppID: 620, Name: "Portal 2", PlaytimeMinutes: 1800, LastPlayedUnix: lastPlayed, HasStats: true}
}

func syncOnce(t *testing.T, a *steam.Adapter, owned []steam.OwnedApp, cur *steam.Cursor) *synctest.Recorder {
	t.Helper()
	rec := &synctest.Recorder{}
	if err := a.SyncAchievementsForTest(context.Background(), acct, owned, cur, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestAchievementsEmitsSchemaAndUnlocks(t *testing.T) {
	f, a := newFixture(t)
	var cur steam.Cursor
	owned := []steam.OwnedApp{
		portal(1700000100),
		{AppID: 400, Name: "Portal", LastPlayedUnix: 5, HasStats: false},
		{AppID: 70, Name: "Half-Life", LastPlayedUnix: 5, HasStats: true},
	}
	rec := syncOnce(t, a, owned, &cur)
	if len(rec.Warnings) != 0 || len(rec.Sets) != 1 {
		t.Fatalf("sets %d warnings %v", len(rec.Sets), rec.Warnings)
	}
	s := rec.Sets[0]
	if s.Key != "620" || len(s.ExternalIDs) != 1 || s.ExternalIDs[0] != (providers.ExternalID{Provider: "steam_app", Value: "620", URL: "https://store.steampowered.com/app/620"}) {
		t.Fatalf("set identity %+v", s)
	}
	if len(s.Achievements) != 3 {
		t.Fatalf("achievements %+v", s.Achievements)
	}
	if a0 := s.Achievements[0]; a0.Key != "ACH_WAKE_UP" || a0.Name != "Wake Up Call" || a0.Order != 0 || a0.Hidden ||
		a0.IconURL != "https://steamcdn-a.akamaihd.net/steamcommunity/public/images/apps/620/wake.jpg" || a0.Description == "" {
		t.Fatalf("first achievement %+v", a0)
	}
	if a2 := s.Achievements[2]; a2.Key != "ACH_SECRET" || !a2.Hidden || a2.Order != 2 {
		t.Fatalf("hidden achievement %+v", a2)
	}
	if len(s.Unlocks) != 1 || s.Unlocks[0].Key != "ACH_WAKE_UP" || s.Unlocks[0].UnlockedAt == nil ||
		!s.Unlocks[0].UnlockedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("unlocks %+v", s.Unlocks)
	}
	if f.count("schema/400") != 0 {
		t.Fatal("fetched a game without community stats")
	}
	if f.count("schema/70") != 1 || f.count("player/70") != 0 {
		t.Fatalf("no-achievement game: schema %d player %d", f.count("schema/70"), f.count("player/70"))
	}

	// Nothing changed: no requests at all, including the remembered no-achievement game.
	before := f.total()
	owned[2].LastPlayedUnix = 99
	if rec := syncOnce(t, a, owned, &cur); len(rec.Sets) != 0 || f.total() != before {
		t.Fatalf("unchanged re-sync emitted %d sets and made %d requests", len(rec.Sets), f.total()-before)
	}
}

func TestAchievementsUnlockWithoutTime(t *testing.T) {
	_, a := newFixture(t)
	var cur steam.Cursor
	rec := syncOnce(t, a, []steam.OwnedApp{{AppID: 10, HasStats: true}}, &cur)
	if len(rec.Sets) != 1 || len(rec.Sets[0].Unlocks) != 1 || rec.Sets[0].Unlocks[0].UnlockedAt != nil {
		t.Fatalf("sets %+v", rec.Sets)
	}
}

func TestAchievementsRefreshRules(t *testing.T) {
	f, a := newFixture(t)
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	defer steam.SetNowForTest(func() time.Time { return clock })()
	var cur steam.Cursor

	syncOnce(t, a, []steam.OwnedApp{portal(100)}, &cur)
	if f.count("player/620") != 1 {
		t.Fatal("first sync didn't fetch")
	}
	clock = t0.Add(6 * 24 * time.Hour)
	syncOnce(t, a, []steam.OwnedApp{portal(100)}, &cur)
	if f.count("schema/620") != 1 || f.count("player/620") != 1 {
		t.Fatal("unchanged rtime_last_played refetched")
	}
	syncOnce(t, a, []steam.OwnedApp{portal(200)}, &cur)
	if f.count("player/620") != 2 {
		t.Fatal("changed rtime_last_played didn't refetch")
	}
	clock = clock.Add(7 * 24 * time.Hour)
	syncOnce(t, a, []steam.OwnedApp{portal(200)}, &cur)
	if f.count("schema/620") != 3 {
		t.Fatal("a schema older than 7 days wasn't refreshed")
	}
}

func TestAchievementsCapPerRun(t *testing.T) {
	f, a := newFixture(t)
	var owned []steam.OwnedApp
	for i := range 250 {
		owned = append(owned, steam.OwnedApp{AppID: int64(1000 + i), LastPlayedUnix: int64(i), HasStats: true})
	}
	var cur steam.Cursor
	rec := syncOnce(t, a, owned, &cur)
	if len(rec.Sets) != 200 {
		t.Fatalf("first run emitted %d sets", len(rec.Sets))
	}
	if f.count("schema/1249") != 1 || f.count("schema/1000") != 0 {
		t.Fatal("most recently played games weren't fetched first")
	}
	rec = syncOnce(t, a, owned, &cur)
	if len(rec.Sets) != 50 || f.count("schema/1000") != 1 {
		t.Fatalf("second run emitted %d sets", len(rec.Sets))
	}
}

func TestAchievementsPrivateStatsStops(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f, a := newFixture(t)
			f.set(func(f *fixture) { f.status["620"], f.status["10"] = status, status })
			var cur steam.Cursor
			owned := []steam.OwnedApp{portal(300), {AppID: 10, LastPlayedUnix: 200, HasStats: true}}
			rec := syncOnce(t, a, owned, &cur)
			if len(rec.Sets) != 0 || len(rec.Warnings) != 1 {
				t.Fatalf("sets %d warnings %v", len(rec.Sets), rec.Warnings)
			}
			if f.count("player/10") != 0 {
				t.Fatal("kept fetching after private stats")
			}
			f.set(func(f *fixture) { delete(f.status, "620"); delete(f.status, "10") })
			if rec := syncOnce(t, a, owned, &cur); len(rec.Sets) != 2 {
				t.Fatalf("not retried after going public: %d sets", len(rec.Sets))
			}
		})
	}
}

func TestAchievementsPerGameErrorContinues(t *testing.T) {
	f, a := newFixture(t)
	f.set(func(f *fixture) { f.status["10"] = http.StatusBadRequest })
	var cur steam.Cursor
	owned := []steam.OwnedApp{{AppID: 10, Name: "Broken", LastPlayedUnix: 900, HasStats: true}, portal(100)}
	rec := syncOnce(t, a, owned, &cur)
	if len(rec.Sets) != 1 || rec.Sets[0].Key != "620" || len(rec.Warnings) != 1 {
		t.Fatalf("sets %+v warnings %v", rec.Sets, rec.Warnings)
	}
	f.set(func(f *fixture) { delete(f.status, "10") })
	rec = syncOnce(t, a, owned, &cur)
	if len(rec.Sets) != 1 || rec.Sets[0].Key != "10" || f.count("player/620") != 1 {
		t.Fatalf("retry: sets %+v", rec.Sets)
	}
}

func TestAchievementsEmitErrorPropagates(t *testing.T) {
	_, a := newFixture(t)
	boom := errors.New("boom")
	var cur steam.Cursor
	err := a.SyncAchievementsForTest(context.Background(), acct, []steam.OwnedApp{portal(1)}, &cur, &synctest.Recorder{Err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// achAdapter runs the real achievements sync behind the runner, emitting the
// owned games itself because syncOwned belongs to S20.
type achAdapter struct {
	*steam.Adapter
	owned []steam.OwnedApp
}

func (x *achAdapter) Sync(ctx context.Context, acct db.ConnectedAccount, cursor []byte, emit sync.Emitter) ([]byte, error) {
	var cur steam.Cursor
	if len(cursor) > 0 {
		if err := json.Unmarshal(cursor, &cur); err != nil {
			return nil, err
		}
	}
	for _, o := range x.owned {
		key := strconv.FormatInt(o.AppID, 10)
		minutes := o.PlaytimeMinutes
		if err := emit.OwnedItem(ctx, sync.OwnedItem{
			Key: key, Title: o.Name, PlaytimeMinutes: &minutes,
			ExternalIDs: []providers.ExternalID{{Provider: steam.ExternalIDProvider, Value: key}},
		}); err != nil {
			return nil, err
		}
	}
	if err := x.SyncAchievementsForTest(ctx, acct, x.owned, &cur, emit); err != nil {
		return nil, err
	}
	return json.Marshal(cur)
}

func (x *achAdapter) Resolve(context.Context, sync.OwnedItem) (*sync.Resolution, error) {
	return nil, nil
}

type runEnv struct {
	s    *testutil.Server
	f    *fixture
	x    *achAdapter
	acct db.ConnectedAccount
}

func newRunEnv(t *testing.T) *runEnv {
	s := testutil.NewServer(t)
	f, a := newFixture(t)
	x := &achAdapter{Adapter: a, owned: []steam.OwnedApp{portal(1000)}}
	s.Deps.SyncAdapters = sync.NewRegistry(x)
	u := testutil.CreateUser(t, s.Q, "alice")
	return &runEnv{s: s, f: f, x: x, acct: testutil.CreateConnectedAccount(t, s.Pool, u.ID, steam.Provider, steamID)}
}

func (e *runEnv) run(t *testing.T) runner.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), e.s.Deps, e.acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *runEnv) scalar(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestAchievementsFixtureSyncThroughRunner(t *testing.T) {
	e := newRunEnv(t)
	if res := e.run(t); res.Status != db.SyncRunStatusSucceeded {
		t.Fatalf("status %s warnings %v err %v", res.Status, res.Warnings, res.Err)
	}
	item := e.scalar(t, `select media_item_id from external_ids where provider = 'steam_app' and value = '620'`)
	if n := e.scalar(t, `select count(*) from achievements where media_item_id = $1 and provider = 'steam' and provider_game_key = '620'`, item); n != 3 {
		t.Fatalf("definitions = %d", n)
	}
	if n := e.scalar(t, `select count(*) from achievements where provider_achievement_key = 'ACH_SECRET' and hidden and display_order = 2`); n != 1 {
		t.Fatal("hidden flag or order not stored")
	}
	var at time.Time
	if err := e.s.Pool.QueryRow(context.Background(), `select u.unlocked_at from achievement_unlocks u
		join achievements a on a.id = u.achievement_id where u.account_id = $1 and a.provider_achievement_key = 'ACH_WAKE_UP'`,
		e.acct.ID).Scan(&at); err != nil || !at.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("unlock time %v: %v", at, err)
	}
	unlockEvents := `select count(*) from sync_events where account_id = $1 and kind = 'achievement_unlocked'`
	if n := e.scalar(t, unlockEvents, e.acct.ID); n != 1 {
		t.Fatalf("events = %d", n)
	}

	// Same rtime_last_played: no Steam achievement requests at all.
	before := e.f.total()
	e.run(t)
	if e.f.total() != before {
		t.Fatalf("unchanged game made %d requests", e.f.total()-before)
	}

	e.f.set(func(f *fixture) { f.player["620"] = "achievements_player_620_more.json" })
	e.x.owned = []steam.OwnedApp{portal(2000)}
	e.run(t)
	if n := e.scalar(t, `select count(*) from achievement_unlocks where account_id = $1`, e.acct.ID); n != 2 {
		t.Fatalf("unlocks = %d", n)
	}
	if n := e.scalar(t, unlockEvents, e.acct.ID); n != 2 {
		t.Fatalf("events after new unlock = %d", n)
	}
	if n := e.scalar(t, unlockEvents+` and field = 'ACH_YOU_MONSTER' and after = 'You Monster'`, e.acct.ID); n != 1 {
		t.Fatal("missing event for the new unlock")
	}
}

func TestAchievementsPrivateKeepsUnlocks(t *testing.T) {
	e := newRunEnv(t)
	e.run(t)
	e.f.set(func(f *fixture) { f.status["620"] = http.StatusForbidden })
	e.x.owned = []steam.OwnedApp{portal(2000)}
	res := e.run(t)
	if res.Status != db.SyncRunStatusPartial || len(res.Warnings) != 1 {
		t.Fatalf("status %s warnings %v", res.Status, res.Warnings)
	}
	if n := e.scalar(t, `select count(*) from achievement_unlocks where account_id = $1`, e.acct.ID); n != 1 {
		t.Fatalf("unlocks after private run = %d", n)
	}
	if n := e.scalar(t, `select count(*) from achievements`); n != 3 {
		t.Fatalf("definitions after private run = %d", n)
	}
}
