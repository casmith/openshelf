package achievements_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/achievements"
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
}

func setup(t *testing.T) *env {
	s := testutil.NewServer(t)
	f := &synctest.Fake{}
	s.Deps.SyncAdapters = sync.NewRegistry(f)
	return &env{s: s, fake: f}
}

var portalDefs = []sync.Achievement{
	{Key: "wake", Name: "Wake Up Call", Description: "Survive the morality core.", IconURL: "https://cdn.steamstatic.com/wake.jpg", Order: 0},
	{Key: "monster", Name: "You Monster", Description: "Reunite with GLaDOS.", IconURL: "https://cdn.steamstatic.com/monster.jpg", Order: 1},
	{Key: "lunacy", Name: "Lunacy", Description: "Secret moon thing.", Hidden: true, Order: 2},
	{Key: "pit", Name: "Pit Boss", Description: "Hidden but unlocked.", Hidden: true, Order: 3},
}

func at(s string) *time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

// syncAccount runs the fake adapter for acct, owning Portal 2 with the given unlocks.
func (e *env) syncAccount(t *testing.T, acct db.ConnectedAccount, unlocks ...sync.Unlock) {
	t.Helper()
	minutes := 60
	e.fake.Games = []sync.OwnedGame{{
		Key: "620", Title: "Portal 2", PlaytimeMinutes: &minutes,
		ExternalIDs: []providers.ExternalID{{Provider: "steam_app", Value: "620"}},
	}}
	e.fake.Sets = []sync.AchievementSet{{Key: "620", Achievements: portalDefs, Unlocks: unlocks}}
	res, err := runner.Run(context.Background(), e.s.Deps, acct.ID)
	if err != nil || res.Status != db.SyncRunStatusSucceeded {
		t.Fatalf("run: %v %s %v", err, res.Status, res.Warnings)
	}
}

func (e *env) portal(t *testing.T) db.MediaItem {
	t.Helper()
	var id int64
	if err := e.s.Pool.QueryRow(context.Background(), `select media_item_id from external_ids where provider = 'steam_app' and value = '620'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	it, err := e.s.Q.GetMediaItemByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func itemPage(t *testing.T, c *testutil.Client, it db.MediaItem) string {
	t.Helper()
	res := c.Get("/items/" + it.PublicID.String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("item page %d", res.StatusCode)
	}
	return res.Body
}

func between(body, start, end string) string {
	_, after, ok := strings.Cut(body, start)
	if !ok {
		return ""
	}
	before, _, _ := strings.Cut(after, end)
	return before
}

func TestItemPanel(t *testing.T) {
	e := setup(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := testutil.CreateConnectedAccount(t, e.s.Pool, alice.ID, "fake", "76561197960287930")
	e.syncAccount(t, acct, sync.Unlock{Key: "wake", UnlockedAt: at("2026-03-04")}, sync.Unlock{Key: "pit"})
	body := itemPage(t, e.s.LoginAs(t, alice), e.portal(t))

	if !strings.Contains(body, `data-testid="achievement-progress">2 / 4<`) {
		t.Fatalf("progress missing:\n%s", between(body, `id="achievements-panel"`, "</section>"))
	}
	if !strings.Contains(body, `role="progressbar"`) || !strings.Contains(body, "width: 50%") {
		t.Fatal("progress bar missing")
	}
	unlocked := between(body, `data-testid="achievements-unlocked"`, "</ul>")
	for _, want := range []string{"Wake Up Call", "Mar 4, 2026", "https://cdn.steamstatic.com/wake.jpg", "Pit Boss", "Hidden but unlocked."} {
		if !strings.Contains(unlocked, want) {
			t.Errorf("unlocked list missing %q", want)
		}
	}
	if strings.Contains(unlocked, "76561197960287930") {
		t.Error("account label shown with a single account")
	}
	locked := between(body, `data-testid="achievements-locked"`, "</ul>")
	if !strings.Contains(locked, "You Monster") || !strings.Contains(locked, "Hidden achievement") {
		t.Errorf("locked list: %s", locked)
	}
	if strings.Contains(body, "Lunacy") || strings.Contains(body, "Secret moon thing.") {
		t.Error("locked hidden achievement revealed")
	}
}

func TestItemPanelLabelsAccounts(t *testing.T) {
	e := setup(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	main := testutil.CreateConnectedAccount(t, e.s.Pool, alice.ID, "fake", "76561197960000001")
	alt := testutil.CreateConnectedAccount(t, e.s.Pool, alice.ID, "fake", "76561197960000002")
	e.syncAccount(t, main, sync.Unlock{Key: "wake", UnlockedAt: at("2026-03-04")})
	e.syncAccount(t, alt, sync.Unlock{Key: "wake", UnlockedAt: at("2026-05-06")}, sync.Unlock{Key: "monster", UnlockedAt: at("2026-05-07")})
	it := e.portal(t)
	body := itemPage(t, e.s.LoginAs(t, alice), it)

	if !strings.Contains(body, `data-testid="achievement-progress">2 / 4<`) {
		t.Fatal("progress should count distinct achievements across accounts")
	}
	unlocked := between(body, `data-testid="achievements-unlocked"`, "</ul>")
	for _, want := range []string{"Mar 4, 2026 · 76561197960000001", "May 6, 2026 · 76561197960000002", "May 7, 2026 · 76561197960000002"} {
		if !strings.Contains(unlocked, want) {
			t.Errorf("unlocked list missing %q:\n%s", want, unlocked)
		}
	}
	counts, err := achievements.Counts(context.Background(), e.s.Deps, alice, []int64{it.ID})
	if err != nil || counts[it.ID] != (achievements.Count{Unlocked: 2, Total: 4}) {
		t.Fatalf("counts %+v: %v", counts, err)
	}
}

func TestItemPanelEmptyWithoutDefinitions(t *testing.T) {
	e := setup(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	it := testutil.CreateItem(t, e.s.Pool, db.MediaKindGame, "No Achievements")
	testutil.CreateEntry(t, e.s.Pool, alice.ID, it.ID)
	if body := itemPage(t, e.s.LoginAs(t, alice), it); strings.Contains(body, "achievements-panel") {
		t.Fatal("panel rendered without definitions")
	}
	c, err := achievements.Counts(context.Background(), e.s.Deps, alice, []int64{it.ID})
	if err != nil || len(c) != 0 {
		t.Fatalf("counts %+v: %v", c, err)
	}
}

func TestCrossUser(t *testing.T) {
	e := setup(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	carol := testutil.CreateUser(t, e.s.Q, "carol")
	e.syncAccount(t, testutil.CreateConnectedAccount(t, e.s.Pool, alice.ID, "fake", "76561197960000001"),
		sync.Unlock{Key: "wake", UnlockedAt: at("2026-03-04")}, sync.Unlock{Key: "monster"}, sync.Unlock{Key: "lunacy"})
	// Bob owns the same game with no unlocks.
	e.syncAccount(t, testutil.CreateConnectedAccount(t, e.s.Pool, bob.ID, "fake", "76561197960000002"))
	it := e.portal(t)

	body := itemPage(t, e.s.LoginAs(t, bob), it)
	if !strings.Contains(body, `data-testid="achievement-progress">0 / 4<`) {
		t.Fatal("bob should see 0 / 4")
	}
	if strings.Contains(body, "achievements-unlocked") || strings.Contains(body, "Mar 4, 2026") || strings.Contains(body, "Lunacy") {
		t.Fatal("bob sees alice's unlocks")
	}
	if body := itemPage(t, e.s.LoginAs(t, carol), it); strings.Contains(body, "achievements-panel") {
		t.Fatal("carol, with no account owning the game, sees the panel")
	}

	ctx := context.Background()
	for _, tc := range []struct {
		user db.User
		want map[int64]achievements.Count
	}{
		{alice, map[int64]achievements.Count{it.ID: {Unlocked: 3, Total: 4}}},
		{bob, map[int64]achievements.Count{it.ID: {Unlocked: 0, Total: 4}}},
		{carol, map[int64]achievements.Count{}},
	} {
		got, err := achievements.Counts(ctx, e.s.Deps, tc.user, []int64{it.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tc.want) || got[it.ID] != tc.want[it.ID] {
			t.Errorf("%s counts %+v, want %+v", tc.user.Username, got, tc.want)
		}
	}
}

func TestCountsIsOneQuery(t *testing.T) {
	e := setup(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	e.syncAccount(t, testutil.CreateConnectedAccount(t, e.s.Pool, alice.ID, "fake", "76561197960000001"), sync.Unlock{Key: "wake"})
	portal := e.portal(t)
	rows, err := e.s.Pool.Query(context.Background(),
		`insert into media_items (kind, title, sort_title) select 'game', 'G' || g, 'G' || g from generate_series(1, 499) g returning id`)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{portal.ID}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil || len(ids) != 500 {
		t.Fatalf("ids %d: %v", len(ids), rows.Err())
	}
	var got map[int64]achievements.Count
	n := testutil.CountQueries(t, e.s.Pool, func() {
		got, err = achievements.Counts(context.Background(), e.s.Deps, alice, ids)
	})
	if err != nil || n != 1 {
		t.Fatalf("queries = %d: %v", n, err)
	}
	if len(got) != 1 || got[portal.ID] != (achievements.Count{Unlocked: 1, Total: 4}) {
		t.Fatalf("counts %+v", got)
	}
}
