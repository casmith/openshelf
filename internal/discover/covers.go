package discover

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/images"
	"github.com/casmith/openshelf/internal/providers"
)

const coverBackfillBatch = 500

// BackfillCovers finds covers for items that have none (e.g. manual or seeded items)
// by searching configured providers for an exact title match. It only sets the cover.
func BackfillCovers(ctx context.Context, d *deps.Deps) (int, error) {
	items, err := d.Q.DiscoverItemsMissingCovers(ctx, coverBackfillBatch)
	if err != nil {
		return 0, err
	}
	found := 0
	for _, it := range items {
		url := findCover(ctx, d, it)
		if url == "" {
			continue
		}
		err := d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
			imageID, err := images.Enqueue(ctx, d, tx, url)
			if err != nil {
				return err
			}
			return q.DiscoverSetCoverIfMissing(ctx, db.DiscoverSetCoverIfMissingParams{ImageID: &imageID, ID: it.ID})
		})
		if err != nil {
			return found, err
		}
		found++
	}
	return found, nil
}

func findCover(ctx context.Context, d *deps.Deps, it db.DiscoverItemsMissingCoversRow) string {
	for _, p := range d.Providers.For(it.Kind) {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		results, err := p.Search(sctx, it.Kind, it.Title)
		cancel()
		recordResult(p.Name(), err, d.Now())
		if err != nil {
			continue
		}
		if r, ok := bestCoverMatch(results, it.Title, it.ReleaseYear); ok {
			return r.CoverURL
		}
	}
	return ""
}

func bestCoverMatch(results []providers.SearchResult, title string, year *int16) (providers.SearchResult, bool) {
	for _, r := range results {
		if r.CoverURL == "" || !strings.EqualFold(strings.TrimSpace(r.Title), strings.TrimSpace(title)) {
			continue
		}
		if year != nil && r.Year != 0 && abs(r.Year-int(*year)) > 1 {
			continue
		}
		return r, true
	}
	return providers.SearchResult{}, false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

type BackfillCoversArgs struct{}

func (BackfillCoversArgs) Kind() string { return "discover_backfill_covers" }

func (BackfillCoversArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

type BackfillCoversWorker struct {
	river.WorkerDefaults[BackfillCoversArgs]
	D *deps.Deps
}

func (wk *BackfillCoversWorker) Work(ctx context.Context, job *river.Job[BackfillCoversArgs]) error {
	n, err := BackfillCovers(ctx, wk.D)
	wk.D.Log.Info("cover backfill finished", "covers", n, "err", err)
	return err
}

func (wk *BackfillCoversWorker) Timeout(*river.Job[BackfillCoversArgs]) time.Duration {
	return time.Hour
}
