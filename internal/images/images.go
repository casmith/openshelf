package images

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web/static"
)

type Size string

const (
	Thumb Size = "thumb" // 200px wide
	Card  Size = "card"  // 400px wide
	Full  Size = "full"  // 800px wide
)

func (s Size) Valid() bool { return s == Thumb || s == Card || s == Full }

// Enqueue records a remote image to fetch and returns its ID. Known URLs return the existing ID.
func Enqueue(ctx context.Context, d *deps.Deps, tx pgx.Tx, sourceURL string) (int64, error) {
	return d.Q.WithTx(tx).ImageUpsertByURL(ctx, sourceURL)
}

// CoverURL is the URL to render for an item or edition cover. The cover handler
// serves a placeholder until the image is ready.
func CoverURL(imageID *int64, kind db.MediaKind, size Size) string {
	if imageID == nil {
		return PlaceholderURL(kind)
	}
	return "/covers/" + strconv.FormatInt(*imageID, 10) + "/" + string(size)
}

func PlaceholderURL(kind db.MediaKind) string {
	return static.URL("placeholder-" + string(kind) + ".svg")
}

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.HandleFunc("GET /covers/{id}/{size}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, static.URL("placeholder-book.svg"), http.StatusFound)
	})
}

func Workers(w *river.Workers, d *deps.Deps) {}
