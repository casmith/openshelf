package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestEnqueueIsUnique(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for range 3 {
		if err := runner.Enqueue(ctx, e.s.Deps, nil, e.acct.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar[int](t, e.s.Pool, `select count(*) from river_job where kind = 'sync_account'`); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
}

func TestScheduleDue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	later := testutil.CreateConnectedAccount(t, e.s.Pool, bob.ID, "fake", "later")
	disabled := testutil.CreateConnectedAccount(t, e.s.Pool, bob.ID, "fake", "disabled")
	other := testutil.CreateConnectedAccount(t, e.s.Pool, bob.ID, "unknown", "x")
	if _, err := e.s.Pool.Exec(ctx, `
		update connected_accounts
		set next_sync_at = case when id = $1 then now() + interval '1 hour' else now() - interval '1 hour' end,
		    status = case when id = $2 then 'disabled'::connection_status else status end`,
		later.ID, disabled.ID); err != nil {
		t.Fatal(err)
	}

	n, err := runner.ScheduleDue(ctx, e.s.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d", n)
	}
	if id := scalar[int64](t, e.s.Pool, `select (args->>'account_id')::bigint from river_job where kind = 'sync_account'`); id != e.acct.ID {
		t.Fatalf("enqueued account %d", id)
	}
	next := scalar[time.Time](t, e.s.Pool, `select next_sync_at from connected_accounts where id = $1`, e.acct.ID)
	if d := time.Until(next); d < 10*time.Hour {
		t.Fatalf("next_sync_at not pushed forward: %v", d)
	}
	if d := time.Until(scalar[time.Time](t, e.s.Pool, `select next_sync_at from connected_accounts where id = $1`, other.ID)); d < 10*time.Hour {
		t.Fatal("unconfigured provider accounts should be pushed forward too")
	}
	if n, _ := runner.ScheduleDue(ctx, e.s.Deps); n != 0 {
		t.Fatalf("second schedule enqueued %d", n)
	}
}

func TestNextSyncJitter(t *testing.T) {
	now := time.Now()
	for range 100 {
		d := runner.NextSync(now, 12*time.Hour).Sub(now)
		if d < 648*time.Minute || d > 792*time.Minute {
			t.Fatalf("out of range: %v", d)
		}
	}
}
