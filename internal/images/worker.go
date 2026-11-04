package images

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/safehttp"
)

type FetchImageArgs struct {
	ImageID int64 `json:"image_id"`
}

func (FetchImageArgs) Kind() string { return "fetch_image" }

func (FetchImageArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type FetchImageWorker struct {
	river.WorkerDefaults[FetchImageArgs]
	D    *deps.Deps
	HTTP *safehttp.Client
}

func (w *FetchImageWorker) Timeout(*river.Job[FetchImageArgs]) time.Duration { return 2 * time.Minute }

// permanentError marks the image failed without retrying.
type permanentError struct{ error }

func (w *FetchImageWorker) Work(ctx context.Context, job *river.Job[FetchImageArgs]) error {
	img, err := w.D.Q.GetImage(ctx, job.Args.ImageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if img.Status == db.ImageStatusReady || img.SourceUrl == nil {
		return nil
	}
	err = w.fetch(ctx, img)
	if err == nil {
		return nil
	}
	var perm permanentError
	if errors.As(err, &perm) || job.Attempt >= job.MaxAttempts {
		msg := err.Error()
		now := w.D.Now()
		if ferr := w.D.Q.ImagesMarkFailed(ctx, db.ImagesMarkFailedParams{ID: img.ID, Error: &msg, FetchedAt: &now}); ferr != nil {
			return ferr
		}
		w.D.Log.Warn("image fetch failed", "image_id", img.ID, "err", err)
		return nil
	}
	return err
}

func (w *FetchImageWorker) fetch(ctx context.Context, img db.Image) error {
	data, _, err := w.HTTP.Get(ctx, *img.SourceUrl)
	if err != nil {
		if errors.Is(err, safehttp.ErrBlocked) || errors.Is(err, safehttp.ErrScheme) || errors.Is(err, safehttp.ErrTooLarge) {
			return permanentError{err}
		}
		return err
	}
	p, err := Process(w.D.Config.DataDir, data)
	if err != nil {
		var pe *processError
		if errors.As(err, &pe) {
			return permanentError{err}
		}
		return err
	}
	return markReady(ctx, w.D.Q, img.ID, p, w.D.Now())
}

func markReady(ctx context.Context, q *db.Queries, id int64, p Processed, now time.Time) error {
	w, h := int32(p.Width), int32(p.Height)
	return q.ImagesMarkReady(ctx, db.ImagesMarkReadyParams{ID: id, Sha256: &p.SHA256, ContentType: &p.ContentType, Width: &w, Height: &h, FetchedAt: &now})
}
