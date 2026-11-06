package synchistory

import (
	"context"
	"time"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

type sourceRow struct {
	db.SynchistoryItemSourcesRow
	ProviderName string
}

type panelView struct {
	Item db.MediaItem
	Rows []sourceRow
	Loc  *time.Location
}

// ItemPanel shows the viewer's synced sources for an item (playtime, last played, exclude).
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	rows, err := d.Q.SynchistoryItemSources(ctx, db.SynchistoryItemSourcesParams{UserID: viewer.ID, MediaItemID: item.ID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return templ.NopComponent, nil
	}
	v := panelView{Item: item, Loc: location(viewer.Timezone)}
	for _, r := range rows {
		v.Rows = append(v.Rows, sourceRow{SynchistoryItemSourcesRow: r, ProviderName: providerName(d, r.Provider)})
	}
	return itemPanel(v), nil
}
