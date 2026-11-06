package steam

import (
	"context"
	"errors"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

var _ sync.Resolver = (*Adapter)(nil)

// Resolve imports the IGDB game hinted by syncOwned. Without a hint, or when
// IGDB isn't configured, the runner creates a manual item for review.
func (a *Adapter) Resolve(ctx context.Context, g sync.OwnedItem) (*sync.Resolution, error) {
	p, _, ok := a.igdb()
	if !ok {
		return nil, nil
	}
	for _, x := range g.ExternalIDs {
		if x.Provider != "igdb_game" || x.Value == "" {
			continue
		}
		it, err := p.Fetch(ctx, db.MediaKindGame, x.Value)
		if errors.Is(err, providers.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &sync.Resolution{Provider: p.Name(), Item: it}, nil
	}
	return nil, nil
}
