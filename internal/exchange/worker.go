package exchange

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const (
	StatusPreviewing = "previewing"
	StatusPreviewed  = "previewed"
	StatusQueued     = "queued"
	StatusDone       = "done"
	StatusFailed     = "failed"
)

// ImportArgs runs an import job. DryRun computes the preview and rolls everything back.
type ImportArgs struct {
	JobID  int64 `json:"job_id"`
	DryRun bool  `json:"dry_run"`
}

func (ImportArgs) Kind() string { return "exchange_import" }

func (ImportArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 3} }

type importWorker struct {
	river.WorkerDefaults[ImportArgs]
	d *deps.Deps
}

func (w *importWorker) Work(ctx context.Context, job *river.Job[ImportArgs]) error {
	return Process(ctx, w.d, job.Args)
}

func Workers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &importWorker{d: d})
}

// Process runs an import job. Re-running a finished job is a no-op.
func Process(ctx context.Context, d *deps.Deps, args ImportArgs) error {
	return d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		job, err := q.ExchangeLoadJobForWork(ctx, args.JobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		want := StatusQueued
		if args.DryRun {
			want = StatusPreviewing
		}
		if job.Status != want {
			return nil
		}

		inner, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		res, runErr := Run(ctx, inner, job.UserID, job.Format, job.Content)
		var fe *fileError
		if runErr != nil && !errors.As(runErr, &fe) {
			_ = inner.Rollback(ctx)
			return runErr
		}
		if args.DryRun || fe != nil {
			err = inner.Rollback(ctx)
		} else {
			err = inner.Commit(ctx)
		}
		if err != nil {
			return err
		}

		p := db.ExchangeFinishJobParams{ID: job.ID, CreatedCount: int32(res.Created), MatchedCount: int32(res.Matched), SkippedCount: int32(res.Skipped)}
		switch {
		case fe != nil:
			p.Status, p.Error = StatusFailed, &fe.msg
			res.Errors = nil
		case args.DryRun:
			p.Status = StatusPreviewed
		default:
			p.Status = StatusDone
		}
		if res.Errors == nil {
			res.Errors = []RowError{}
		}
		if p.RowErrors, err = json.Marshal(res.Errors); err != nil {
			return err
		}
		return q.ExchangeFinishJob(ctx, p)
	})
}
