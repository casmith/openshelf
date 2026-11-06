package catalog

import (
	"context"
	"strings"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

// Header renders the top of the item detail page: cover, title, credits, details, editions.
func Header(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	details, err := LoadItemInput(ctx, d.Q, item)
	if err != nil {
		return nil, err
	}
	editions, err := d.Q.ListEditionsForItem(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	h := headerData{Item: item, Details: details, Editions: editions, CanEdit: CanEdit(viewer, item),
		CanRefresh: viewer.Role == db.UserRoleAdmin && item.MetadataProvider != nil}
	if item.Kind == db.MediaKindGame {
		if h.TimeToBeat, err = noRows(d.Q.CatalogGetTimeToBeat(ctx, item.ID)); err != nil {
			return nil, err
		}
		if h.PlayedMinutes, err = d.Q.CatalogViewerPlaytime(ctx, db.CatalogViewerPlaytimeParams{MediaItemID: item.ID, UserID: viewer.ID}); err != nil {
			return nil, err
		}
	}
	return header(h), nil
}

func KindLabel(k db.MediaKind) string {
	switch k {
	case db.MediaKindGame:
		return "Game"
	case db.MediaKindBook:
		return "Book"
	case db.MediaKindFilm:
		return "Movie"
	case db.MediaKindShow:
		return "TV show"
	}
	return string(k)
}

var leadingArticles = []string{"the ", "a ", "an "}

// SortTitle moves a leading English article to the end: "The Expanse" -> "Expanse, The".
func SortTitle(title string) string {
	lower := strings.ToLower(title)
	for _, a := range leadingArticles {
		if strings.HasPrefix(lower, a) && len(title) > len(a) {
			return strings.TrimSpace(title[len(a):]) + ", " + strings.TrimSpace(title[:len(a)])
		}
	}
	return title
}
