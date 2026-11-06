package library

import (
	"context"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/achievements"
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
	return itemPanel(panelView{Item: item, Entries: entries, Editions: editions, Viewer: viewer, Src: sourceNames(d)}), nil
}

// sources labels values that came from a linked account: provider display names,
// plus achievement counts for library cards.
type sources struct {
	Names  map[string]string
	Counts map[int64]achievements.Count
}

func sourceNames(d *deps.Deps) sources {
	s := sources{Names: map[string]string{}}
	for _, a := range d.SyncAdapters.All() {
		s.Names[a.Provider()] = a.Capabilities().DisplayName
	}
	return s
}

func (s sources) name(provider string) string {
	if n := s.Names[provider]; n != "" {
		return n
	}
	if provider != "" {
		return provider
	}
	return "a linked account"
}

// loadSources fetches achievement counts only for synced entries, so libraries
// without linked accounts don't pay for the query.
func loadSources(ctx context.Context, d *deps.Deps, viewer db.User, entries []db.LibraryListEntriesRow) (sources, error) {
	s := sourceNames(d)
	var ids []int64
	for _, e := range entries {
		if e.SourceProvider != "" {
			ids = append(ids, e.MediaItemID)
		}
	}
	if len(ids) == 0 {
		return s, nil
	}
	counts, err := achievements.Counts(ctx, d, viewer, ids)
	if err != nil {
		return s, err
	}
	s.Counts = counts
	return s, nil
}
