package discover_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
	"github.com/casmith/openshelf/internal/testutil"
)

func gameItem(id, title string) *providers.Item {
	return &providers.Item{
		Kind: db.MediaKindGame, Title: title,
		ExternalIDs: []providers.ExternalID{{Provider: "igdb_game", Value: id}},
		Game:        &providers.GameDetails{},
	}
}

type ttbRow struct {
	Hastily, Normally, Completely *int32
	Submissions                   *int32
	Source                        string
}

func getTTB(t *testing.T, s *testutil.Server, itemID int64) (ttbRow, bool) {
	t.Helper()
	var r ttbRow
	err := s.Pool.QueryRow(context.Background(),
		`select hastily_minutes, normally_minutes, completely_minutes, submissions, source from game_time_to_beat where media_item_id = $1`, itemID).
		Scan(&r.Hastily, &r.Normally, &r.Completely, &r.Submissions, &r.Source)
	if err != nil {
		return r, false
	}
	return r, true
}

func TestImportUpsertsTimeToBeat(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	it := gameItem("1072", "Rez")
	it.Game.TimeToBeat = &providers.TimeToBeat{HastilyMinutes: i32(65), NormallyMinutes: i32(149), Submissions: 13}
	item, err := discover.Import(ctx, s.Deps, "igdb", it, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := getTTB(t, s, item.ID)
	if !ok || *r.Hastily != 65 || *r.Normally != 149 || r.Completely != nil || *r.Submissions != 13 || r.Source != "igdb" {
		t.Fatalf("after import: %+v %v", r, ok)
	}

	it.Game.TimeToBeat = &providers.TimeToBeat{NormallyMinutes: i32(180), CompletelyMinutes: i32(300), Submissions: 20}
	if _, err := discover.Import(ctx, s.Deps, "igdb", it, nil); err != nil {
		t.Fatal(err)
	}
	r, _ = getTTB(t, s, item.ID)
	if r.Hastily != nil || *r.Normally != 180 || *r.Completely != 300 || *r.Submissions != 20 {
		t.Fatalf("after refresh: %+v", r)
	}

	it.Game.TimeToBeat = nil
	if _, err := discover.Import(ctx, s.Deps, "igdb", it, nil); err != nil {
		t.Fatal(err)
	}
	if r, ok := getTTB(t, s, item.ID); !ok || *r.Normally != 180 {
		t.Fatalf("estimate removed when absent from fetch: %+v %v", r, ok)
	}
}

type ttbProvider struct {
	*providertest.Fake
	estimates map[string]providers.TimeToBeat
	calls     [][]string
}

func (p *ttbProvider) TimesToBeat(ctx context.Context, ids []string) (map[string]providers.TimeToBeat, error) {
	p.calls = append(p.calls, ids)
	out := map[string]providers.TimeToBeat{}
	for _, id := range ids {
		if t, ok := p.estimates[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func TestBackfillTimeToBeat(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	p := &ttbProvider{Fake: &providertest.Fake{ProviderName: "igdb", Kind: db.MediaKindGame}, estimates: map[string]providers.TimeToBeat{}}
	s.Deps.Providers = providers.NewRegistry(p)

	var items []db.MediaItem
	for i := range 3 {
		item, err := discover.Import(ctx, s.Deps, "igdb", gameItem(fmt.Sprint(100+i), fmt.Sprint("Game ", i)), nil)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Manual game")
	p.estimates["100"] = providers.TimeToBeat{NormallyMinutes: i32(600), Submissions: 4}
	p.estimates["101"] = providers.TimeToBeat{HastilyMinutes: i32(30), Submissions: 1}

	n, err := discover.BackfillTimeToBeat(ctx, s.Deps, 2)
	if err != nil || n != 2 || len(p.calls) != 1 || len(p.calls[0]) != 2 {
		t.Fatalf("bounded batch: n=%d err=%v calls=%v", n, err, p.calls)
	}
	n, err = discover.BackfillTimeToBeat(ctx, s.Deps, 0)
	if err != nil || n != 0 || len(p.calls) != 2 || fmt.Sprint(p.calls[1]) != "[102]" {
		t.Fatalf("all: n=%d err=%v calls=%v", n, err, p.calls)
	}
	if r, ok := getTTB(t, s, items[0].ID); !ok || *r.Normally != 600 {
		t.Fatalf("item 0: %+v %v", r, ok)
	}
	if r, ok := getTTB(t, s, items[2].ID); !ok || r.Normally != nil || r.Hastily != nil {
		t.Fatalf("game without estimate should be marked checked: %+v %v", r, ok)
	}

	n, err = discover.BackfillTimeToBeat(ctx, s.Deps, 0)
	if err != nil || n != 0 || len(p.calls) != 2 {
		t.Fatalf("second run not idempotent: n=%d err=%v calls=%d", n, err, len(p.calls))
	}

	if _, err := s.Pool.Exec(ctx, `update game_time_to_beat set refreshed_at = now() - interval '31 days' where media_item_id = $1`, items[0].ID); err != nil {
		t.Fatal(err)
	}
	p.estimates["100"] = providers.TimeToBeat{NormallyMinutes: i32(660), Submissions: 5}
	if n, err := discover.BackfillTimeToBeat(ctx, s.Deps, 0); err != nil || n != 1 || fmt.Sprint(p.calls[2]) != "[100]" {
		t.Fatalf("stale refresh: n=%d err=%v calls=%v", n, err, p.calls)
	}
	if r, _ := getTTB(t, s, items[0].ID); *r.Normally != 660 {
		t.Fatalf("stale estimate not updated: %+v", r)
	}
}

func TestBackfillTimeToBeatAdminButton(t *testing.T) {
	s := testutil.NewServer(t)
	admin := testutil.CreateUser(t, s.Q, "admin", testutil.Admin)
	member := testutil.CreateUser(t, s.Q, "bob")
	if res := s.LoginAs(t, member).PostForm("/admin/providers/time-to-beat", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member: %d", res.StatusCode)
	}
	c := s.LoginAs(t, admin)
	if res := c.Get("/admin/providers"); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "Fetch time-to-beat estimates") {
		t.Fatalf("admin page: %d", res.StatusCode)
	}
	for range 2 {
		if res := c.PostForm("/admin/providers/time-to-beat", nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("enqueue: %d", res.StatusCode)
		}
	}
	if n := count(t, s, `select count(*) from river_job where kind = 'discover_time_to_beat'`); n != 1 {
		t.Fatalf("%d jobs, want 1 (unique while pending)", n)
	}
}
