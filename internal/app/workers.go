package app

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/deps"
)

// registerWorkers adds every feature package's River workers.
func registerWorkers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &noopWorker{})
}

func periodicJobs() []*river.PeriodicJob {
	return nil
}

// NoopArgs keeps the worker bundle non-empty until feature workers exist; also used by tests.
type NoopArgs struct{}

func (NoopArgs) Kind() string { return "noop" }

type noopWorker struct {
	river.WorkerDefaults[NoopArgs]
}

func (*noopWorker) Work(ctx context.Context, job *river.Job[NoopArgs]) error { return nil }
