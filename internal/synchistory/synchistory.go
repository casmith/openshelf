// Package synchistory serves sync history, exclusions, and unlinking (S22).
package synchistory

import (
	"context"
	"net/http"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

func Register(mux *http.ServeMux, d *deps.Deps) {}

// ItemPanel shows the viewer's synced sources for an item (playtime, last played, exclude).
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return templ.NopComponent, nil
}
