// Package synctest provides an in-memory sync adapter for tests.
package synctest

import (
	"context"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
)

// Fake emits its configured observations in order, then returns Cursor and Err.
type Fake struct {
	Name         string // provider; defaults to "fake"
	Unconfigured bool
	Caps         sync.Capabilities
	Games        []sync.OwnedGame
	Sets         []sync.AchievementSet
	Warnings     []string
	Cursor       []byte
	Err          error
	ResolveFunc  func(ctx context.Context, g sync.OwnedGame) (*sync.Resolution, error)

	Calls      int
	LastCursor []byte // the cursor passed to the most recent Sync
}

var (
	_ sync.Adapter  = (*Fake)(nil)
	_ sync.Resolver = (*Fake)(nil)
)

func (f *Fake) Provider() string {
	if f.Name == "" {
		return "fake"
	}
	return f.Name
}

func (f *Fake) Configured() bool { return !f.Unconfigured }

func (f *Fake) Capabilities() sync.Capabilities {
	if f.Caps.DisplayName == "" {
		return sync.Capabilities{DisplayName: "Fake", LinkPath: "/connections/fake/link", OwnedGames: true, Playtime: true, Achievements: true}
	}
	return f.Caps
}

func (f *Fake) Sync(ctx context.Context, acct db.ConnectedAccount, cursor []byte, emit sync.Emitter) ([]byte, error) {
	f.Calls++
	f.LastCursor = cursor
	for _, w := range f.Warnings {
		emit.Warn(w)
	}
	for _, g := range f.Games {
		if err := emit.OwnedGame(ctx, g); err != nil {
			return nil, err
		}
	}
	for _, s := range f.Sets {
		if err := emit.Achievements(ctx, s); err != nil {
			return nil, err
		}
	}
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Cursor, nil
}

func (f *Fake) Resolve(ctx context.Context, g sync.OwnedGame) (*sync.Resolution, error) {
	if f.ResolveFunc == nil {
		return nil, nil
	}
	return f.ResolveFunc(ctx, g)
}
