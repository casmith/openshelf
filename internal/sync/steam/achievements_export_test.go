package steam

import (
	"context"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
)

func (a *Adapter) SyncAchievementsForTest(ctx context.Context, acct db.ConnectedAccount, owned []OwnedApp, cur *Cursor, emit sync.Emitter) error {
	return a.syncAchievements(ctx, acct, owned, cur, emit)
}

func SetNowForTest(f func() time.Time) (restore func()) {
	old := now
	now = f
	return func() { now = old }
}
