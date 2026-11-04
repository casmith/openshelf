package library

import (
	"context"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

// ItemPanel renders the viewer's library entries for an item on the item page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	entries, err := d.Q.LibraryItemEntries(ctx, db.LibraryItemEntriesParams{UserID: viewer.ID, MediaItemID: item.ID})
	if err != nil {
		return nil, err
	}
	editions, err := d.Q.ListEditionsForItem(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	return itemPanel(panelView{Item: item, Entries: entries, Editions: editions, Viewer: viewer}), nil
}
