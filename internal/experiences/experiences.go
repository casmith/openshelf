package experiences

import (
	"context"
	"net/http"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

// Register adds this package's routes. Owned by task S03 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {}

// ItemPanel renders this package's section of the item detail page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return templ.NopComponent, nil
}

// QuickLog renders an inline progress/finish control for dashboard cards (used by S06).
func QuickLog(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return templ.NopComponent, nil
}
