package runner

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const (
	ScheduleEvery = 15 * time.Minute
	scheduleBatch = 200
	jobTimeout    = 30 * time.Minute
)

// SyncAccountArgs syncs one connected account. Only one can be pending or running per account.
type SyncAccountArgs struct {
	AccountID int64 `json:"account_id"`
}

func (SyncAccountArgs) Kind() string { return "sync_account" }

func (SyncAccountArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
}

type syncAccountWorker struct {
	river.WorkerDefaults[SyncAccountArgs]
	d *deps.Deps
}

func (w *syncAccountWorker) Work(ctx context.Context, job *river.Job[SyncAccountArgs]) error {
	_, err := Run(ctx, w.d, job.Args.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (w *syncAccountWorker) Timeout(*river.Job[SyncAccountArgs]) time.Duration { return jobTimeout }

// ScheduleSyncsArgs is the periodic job that enqueues accounts whose next_sync_at has passed.
type ScheduleSyncsArgs struct{}

func (ScheduleSyncsArgs) Kind() string { return "sync_schedule" }

type scheduleWorker struct {
	river.WorkerDefaults[ScheduleSyncsArgs]
	d *deps.Deps
}

func (w *scheduleWorker) Work(ctx context.Context, job *river.Job[ScheduleSyncsArgs]) error {
	_, err := ScheduleDue(ctx, w.d)
	return err
}

func Workers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &syncAccountWorker{d: d})
	river.AddWorker(w, &scheduleWorker{d: d})
}

func PeriodicJobs(d *deps.Deps) []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(ScheduleEvery), func() (river.JobArgs, *river.InsertOpts) {
			return ScheduleSyncsArgs{}, nil
		}, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}

// Enqueue queues a sync for the account now ("Sync now"). Pass the caller's
// transaction when there is one; a sync already pending is not duplicated.
func Enqueue(ctx context.Context, d *deps.Deps, tx pgx.Tx, accountID int64) error {
	args := SyncAccountArgs{AccountID: accountID}
	var err error
	if tx != nil {
		_, err = d.River.InsertTx(ctx, tx, args, nil)
	} else {
		_, err = d.River.Insert(ctx, args, nil)
	}
	return err
}

// ScheduleDue enqueues syncs for due accounts and pushes their next_sync_at forward.
// Accounts whose adapter isn't configured are pushed forward without a job.
func ScheduleDue(ctx context.Context, d *deps.Deps) (int, error) {
	n := 0
	err := d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		now := d.Now()
		due, err := q.SyncDueAccounts(ctx, db.SyncDueAccountsParams{Now: now, Lim: scheduleBatch})
		if err != nil {
			return err
		}
		for _, a := range due {
			next := NextSync(now, DefaultInterval)
			if ad, ok := d.SyncAdapters.Get(a.Provider); ok && ad.Configured() {
				if err := Enqueue(ctx, d, tx, a.ID); err != nil {
					return err
				}
				n++
			}
			if err := q.SyncSetNextSync(ctx, db.SyncSetNextSyncParams{ID: a.ID, NextSyncAt: next}); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
