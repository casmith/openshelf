package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

func TestManualItemTitleFixAndRematch(t *testing.T) {
	e := setup(t)
	var match bool
	e.fake.ResolveFunc = func(ctx context.Context, g sync.OwnedGame) (*sync.Resolution, error) {
		if !match {
			return nil, nil
		}
		return &sync.Resolution{Provider: "igdb", Item: &providers.Item{
			Kind: db.MediaKindGame, Title: "Portal 2", ReleaseYear: 2011,
			ExternalIDs: []providers.ExternalID{{Provider: "igdb_game", Value: "72"}},
		}}, nil
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2 (Beta)", ip(10))}
	e.run(t)
	id := itemFor(t, e, "620")
	userAction(t, e.s.Pool, `update library_entries set played_user = false, backlog = true, notes = 'mine' where media_item_id = $1`, id)

	e.fake.Games[0].Title = "Portal 2"
	if res := e.run(t); res.Err != nil || res.Counts.ItemsResolved != 0 {
		t.Fatalf("title fix %+v %v", res.Counts, res.Err)
	}
	if got := scalar[string](t, e.s.Pool, `select title || '|' || sort_title from media_items where id = $1`, id); got != "Portal 2|Portal 2" || countEvents(t, e, sync.EventTitleChanged) != 1 {
		t.Fatalf("title %q", got)
	}

	match = true
	e.fake.Games[0].Title = "Portal 2 Deluxe"
	if res := e.run(t); res.Err != nil || res.Counts.ItemsResolved != 1 || res.Counts.ItemsCreated != 0 {
		t.Fatalf("rematch %+v %v", res.Counts, res.Err)
	}
	if got := scalar[string](t, e.s.Pool, `select title || '|' || metadata_provider from media_items where id = $1`, id); got != "Portal 2|igdb" {
		t.Errorf("rematched %q", got)
	}
	if r := entry(t, e, e.user.ID, id); *r.PlayedUser || !r.Backlog {
		t.Errorf("user data changed %+v", r)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from media_items`); n != 1 {
		t.Errorf("%d items", n)
	}
	if n := countEvents(t, e, sync.EventItemMatched); n != 1 {
		t.Errorf("%d match events", n)
	}

	// Provider items are left alone.
	e.fake.Games[0].Title = "Portal Two"
	e.run(t)
	if got := scalar[string](t, e.s.Pool, `select title from media_items where id = $1`, id); got != "Portal 2" {
		t.Errorf("provider item title %q", got)
	}
}

func TestDeferredResolution(t *testing.T) {
	e := setup(t)
	e.fake.ResolveFunc = func(ctx context.Context, g sync.OwnedGame) (*sync.Resolution, error) {
		return nil, sync.ErrDeferred
	}
	e.fake.Games = []sync.OwnedGame{steamGame("620", "Portal 2", ip(10)), steamGame("500", "Left 4 Dead", ip(10))}
	res := e.run(t)
	if res.Counts.Deferred != 2 || res.Counts.ItemsCreated != 0 || res.Counts.Skipped != 0 || res.Status != db.SyncRunStatusPartial {
		t.Fatalf("counts %+v status %s", res.Counts, res.Status)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "2 games queued for matching next sync" {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from media_items`); n != 0 {
		t.Errorf("deferred items created %d items", n)
	}
	next := scalar[time.Time](t, e.s.Pool, `select next_sync_at from connected_accounts where id = $1`, e.acct.ID)
	if d := time.Until(next); d > 20*time.Minute || d < 10*time.Minute {
		t.Errorf("next sync in %s", d)
	}
}
