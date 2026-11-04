package discover

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/deps"
)

const (
	staleAfter   = 30 * 24 * time.Hour
	refreshBatch = 50
)

// RefreshStaleArgs is the periodic job that refreshes metadata older than staleAfter.
type RefreshStaleArgs struct{}

func (RefreshStaleArgs) Kind() string { return "discover_refresh_stale" }

type RefreshStaleWorker struct {
	river.WorkerDefaults[RefreshStaleArgs]
	D *deps.Deps
}

func (wk *RefreshStaleWorker) Work(ctx context.Context, job *river.Job[RefreshStaleArgs]) error {
	_, err := RefreshStale(ctx, wk.D, wk.D.Now().Add(-staleAfter), refreshBatch)
	return err
}

func (wk *RefreshStaleWorker) Timeout(*river.Job[RefreshStaleArgs]) time.Duration {
	return 30 * time.Minute
}

func Workers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &RefreshStaleWorker{D: d})
}

func PeriodicJobs(d *deps.Deps) []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) {
			return RefreshStaleArgs{}, nil
		}, nil),
	}
}
