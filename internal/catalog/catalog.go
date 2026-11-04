package catalog

import (
	"context"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

// Register adds this package's routes. Owned by task S01 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {}

type headerData struct {
	Item     db.MediaItem
	Credits  []db.ListCreditsForItemsRow
	Editions []db.ListEditionsForItemRow
}

// Header renders the top of the item detail page: cover, title, credits, synopsis, editions.
func Header(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	credits, err := d.Q.ListCreditsForItems(ctx, []int64{item.ID})
	if err != nil {
		return nil, err
	}
	editions, err := d.Q.ListEditionsForItem(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	return header(headerData{Item: item, Credits: credits, Editions: editions}), nil
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
