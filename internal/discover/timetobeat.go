package discover

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/providers"
)

const (
	timeToBeatBatch    = 500
	timeToBeatProvider = "igdb"
)

// TimeToBeatSource is implemented by providers that can look up estimates in bulk (IGDB).
type TimeToBeatSource interface {
	TimesToBeat(ctx context.Context, gameIDs []string) (map[string]providers.TimeToBeat, error)
}

func upsertTimeToBeat(ctx context.Context, q *db.Queries, itemID int64, source string, t providers.TimeToBeat) error {
	return q.DiscoverUpsertTimeToBeat(ctx, db.DiscoverUpsertTimeToBeatParams{
		MediaItemID: itemID, Source: source, HastilyMinutes: t.HastilyMinutes, NormallyMinutes: t.NormallyMinutes,
		CompletelyMinutes: t.CompletelyMinutes, Submissions: &t.Submissions,
	})
}

// BackfillTimeToBeat fetches estimates for games with an IGDB ID that have none or
// a stale one, in batches, until limit items are processed (0 means all). Games
// IGDB has no estimate for are marked checked so they're retried only once stale.
func BackfillTimeToBeat(ctx context.Context, d *deps.Deps, limit int) (int, error) {
	p, ok := d.Providers.Get(timeToBeatProvider)
	src, isSrc := p.(TimeToBeatSource)
	if !ok || !isSrc || !p.Configured() {
		return 0, ErrNoProvider
	}
	cutoff := d.Now().Add(-staleAfter)
	processed, found := 0, 0
	for limit == 0 || processed < limit {
		n := timeToBeatBatch
		if limit > 0 {
			n = min(n, limit-processed)
		}
		rows, err := d.Q.DiscoverItemsNeedingTimeToBeat(ctx, db.DiscoverItemsNeedingTimeToBeatParams{Cutoff: cutoff, Lim: int32(n)})
		if err != nil || len(rows) == 0 {
			return found, err
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.IgdbID
		}
		ttb, err := src.TimesToBeat(ctx, ids)
		recordResult(p.Name(), err, d.Now())
		if err != nil {
			return found, err
		}
		var missing []int64
		for _, r := range rows {
			t, ok := ttb[r.IgdbID]
			if !ok {
				missing = append(missing, r.ID)
				continue
			}
			if err := upsertTimeToBeat(ctx, d.Q, r.ID, p.Name(), t); err != nil {
				return found, err
			}
			found++
		}
		if len(missing) > 0 {
			if err := d.Q.DiscoverMarkTimeToBeatChecked(ctx, missing); err != nil {
				return found, err
			}
		}
		processed += len(rows)
		if len(rows) < n {
			break
		}
	}
	return found, nil
}

// TimeToBeatArgs backfills time-to-beat estimates; Limit 0 processes every eligible game.
type TimeToBeatArgs struct {
	Limit int `json:"limit"`
}

func (TimeToBeatArgs) Kind() string { return "discover_time_to_beat" }

func (TimeToBeatArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

type TimeToBeatWorker struct {
	river.WorkerDefaults[TimeToBeatArgs]
	D *deps.Deps
}

func (wk *TimeToBeatWorker) Work(ctx context.Context, job *river.Job[TimeToBeatArgs]) error {
	n, err := BackfillTimeToBeat(ctx, wk.D, job.Args.Limit)
	if errors.Is(err, ErrNoProvider) {
		return nil
	}
	wk.D.Log.Info("time-to-beat backfill finished", "estimates", n, "err", err)
	return err
}

func (wk *TimeToBeatWorker) Timeout(*river.Job[TimeToBeatArgs]) time.Duration {
	return time.Hour
}
