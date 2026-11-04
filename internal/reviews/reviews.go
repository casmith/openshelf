package reviews

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

// Register adds this package's routes. Owned by task S04 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {}

// ItemPanel renders this package's section of the item detail page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return templ.NopComponent, nil
}

// RatingDisplay renders a stored 0–100 rating in the viewer's display scale (used by S02, S06, S07).
func RatingDisplay(display db.RatingDisplay, rating *int16) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if rating == nil {
			return nil
		}
		_, err := io.WriteString(w, strconv.Itoa(int(*rating))+"/100")
		return err
	})
}

// RatingInput renders a rating form control named name for the given display scale.
func RatingInput(name string, display db.RatingDisplay, value *int16) templ.Component {
	return templ.NopComponent
}
