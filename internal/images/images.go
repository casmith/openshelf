package images

import (
	"context"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/safehttp"
	"github.com/casmith/openshelf/internal/web/static"
)

type Size string

const (
	Thumb Size = "thumb" // 200px wide
	Card  Size = "card"  // 400px wide
	Full  Size = "full"  // 800px wide
)

func (s Size) Valid() bool { return s == Thumb || s == Card || s == Full }

// Enqueue records a remote image to fetch and returns its ID. Known URLs return
// the existing ID; a fetch job is queued unless the image is ready or one is pending.
func Enqueue(ctx context.Context, d *deps.Deps, tx pgx.Tx, sourceURL string) (int64, error) {
	row, err := d.Q.WithTx(tx).ImagesUpsertByURL(ctx, sourceURL)
	if err != nil {
		return 0, err
	}
	if row.Status == db.ImageStatusReady {
		return row.ID, nil
	}
	_, err = d.River.InsertTx(ctx, tx, FetchImageArgs{ImageID: row.ID}, nil)
	return row.ID, err
}

// CoverURL is the URL to render for an item or edition cover. The cover handler
// serves the kind's placeholder until the image is ready.
func CoverURL(imageID *int64, kind db.MediaKind, size Size) string {
	if imageID == nil {
		return PlaceholderURL(kind)
	}
	return "/covers/" + strconv.FormatInt(*imageID, 10) + "/" + string(size) + "?k=" + url.QueryEscape(string(kind))
}

func PlaceholderURL(kind db.MediaKind) string {
	if !kind.Valid() {
		kind = db.MediaKindBook
	}
	return static.URL("placeholder-" + string(kind) + ".svg")
}

func Workers(w *river.Workers, d *deps.Deps) {
	river.AddWorker(w, &FetchImageWorker{D: d, HTTP: safehttp.New(safehttp.Options{})})
}
