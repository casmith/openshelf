// Package achievements shows synced achievements on the item page and library (S21).
package achievements

import (
	"context"
	"net/http"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

func Register(mux *http.ServeMux, d *deps.Deps) {}

// ItemPanel renders the viewer's achievements for an item on the item page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return templ.NopComponent, nil
}

// Count is the viewer's unlocked/total achievements for one item across their linked accounts.
type Count struct {
	Unlocked int
	Total    int
}

// Counts returns achievement counts for items that have any, in one query (library cards call it).
func Counts(ctx context.Context, d *deps.Deps, viewer db.User, itemIDs []int64) (map[int64]Count, error) {
	return map[int64]Count{}, nil
}
