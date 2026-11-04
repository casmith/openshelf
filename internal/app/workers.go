package app

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/exchange"
	"github.com/casmith/openshelf/internal/images"
)

func registerWorkers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &noopWorker{})
	images.Workers(w, d)
	discover.Workers(w, d)
	exchange.Workers(w, d)
}

func periodicJobs(d *deps.Deps) []*river.PeriodicJob {
	var jobs []*river.PeriodicJob
	jobs = append(jobs, discover.PeriodicJobs(d)...)
	return jobs
}

// NoopArgs keeps the worker bundle non-empty; tests may enqueue it too.
type NoopArgs struct{}

func (NoopArgs) Kind() string { return "noop" }

type noopWorker struct {
	river.WorkerDefaults[NoopArgs]
}

func (*noopWorker) Work(ctx context.Context, job *river.Job[NoopArgs]) error { return nil }
