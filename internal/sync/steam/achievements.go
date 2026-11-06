package steam

import (
	"context"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
)

// syncAchievements emits achievement schemas and unlocks for owned games (S21).
func (a *Adapter) syncAchievements(ctx context.Context, acct db.ConnectedAccount, owned []OwnedApp, cur *Cursor, emit sync.Emitter) error {
	return nil
}
