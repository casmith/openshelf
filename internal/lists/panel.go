package lists

import (
	"context"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/images"
)

// ItemPanel shows which of the viewer's lists hold item and an "Add to list" menu.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	rows, err := d.Q.ListsForOwnerWithItem(ctx, db.ListsForOwnerWithItemParams{MediaItemID: item.ID, UserID: viewer.ID})
	if err != nil {
		return nil, err
	}
	var in, out []db.ListsForOwnerWithItemRow
	for _, r := range rows {
		if r.HasItem {
			in = append(in, r)
		} else {
			out = append(out, r)
		}
	}
	return panel(item, in, out), nil
}

type listCard struct {
	ID         int64
	PublicID   uuid.UUID
	Name       string
	Owner      string
	Count      int64
	Ranked     bool
	Visibility db.Visibility
	Covers     []string
}

type listData struct {
	List  db.ListsGetVisibleRow
	Items []db.ListsItemsRow
	Owner bool
}

func coverThumb(id *int64, kind db.MediaKind) string { return images.CoverURL(id, kind, images.Thumb) }

func itemURL(id uuid.UUID) string { return "/items/" + id.String() }

func visibilityLabel(v db.Visibility) string {
	switch v {
	case db.VisibilityInstance:
		return "Everyone on this server"
	case db.VisibilityPublic:
		return "Public"
	}
	return "Only me"
}
