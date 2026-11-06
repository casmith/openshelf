package audiobookshelf

import (
	"context"
	"errors"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
	"github.com/casmith/openshelf/internal/sync"
)

// Resolve maps a book to an Open Library work by ISBN, then by exact title and
// first author. Without a match the runner creates a manual book item.
func (a *Adapter) Resolve(ctx context.Context, g sync.OwnedItem) (*sync.Resolution, error) {
	if g.ItemKind() != db.MediaKindBook || a.Metadata == nil {
		return nil, nil
	}
	p, ok := a.Metadata.Get("openlibrary")
	if !ok || !p.Configured() {
		return nil, nil
	}
	ol, ok := p.(*openlibrary.Provider)
	if !ok {
		return nil, nil
	}
	workID, err := a.findWork(ctx, ol, g)
	if errors.Is(err, providers.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it, err := ol.Fetch(ctx, db.MediaKindBook, workID)
	if errors.Is(err, providers.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sync.Resolution{Provider: ol.Name(), Item: it}, nil
}

func (a *Adapter) findWork(ctx context.Context, ol *openlibrary.Provider, g sync.OwnedItem) (string, error) {
	if g.Edition != nil {
		for _, x := range g.Edition.ExternalIDs {
			if x.Provider != "isbn13" && x.Provider != "isbn10" {
				continue
			}
			id, err := ol.FindByISBN(ctx, x.Value)
			if errors.Is(err, providers.ErrNotFound) {
				continue
			}
			return id, err
		}
	}
	for _, c := range g.Credits {
		if c.Role == db.CreditRoleAuthor && c.Name != "" {
			return ol.FindWork(ctx, g.Title, c.Name)
		}
	}
	return "", providers.ErrNotFound
}
