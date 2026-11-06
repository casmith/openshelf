package steam

import (
	"context"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
)

// syncOwned emits the account's owned games (S20).
func (a *Adapter) syncOwned(ctx context.Context, acct db.ConnectedAccount, cur *Cursor, emit sync.Emitter) ([]OwnedApp, error) {
	return nil, nil
}
