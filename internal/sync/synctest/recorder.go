package synctest

import (
	"context"

	"github.com/casmith/openshelf/internal/sync"
)

// Recorder is an Emitter that collects observations, for adapter unit tests.
// Set Err to make every emit fail (e.g. to check adapters propagate it).
type Recorder struct {
	Games    []sync.OwnedItem
	Sets     []sync.AchievementSet
	Warnings []string
	Err      error
}

var _ sync.Emitter = (*Recorder)(nil)

func (r *Recorder) OwnedItem(ctx context.Context, g sync.OwnedItem) error {
	if r.Err != nil {
		return r.Err
	}
	r.Games = append(r.Games, g)
	return nil
}

func (r *Recorder) Achievements(ctx context.Context, s sync.AchievementSet) error {
	if r.Err != nil {
		return r.Err
	}
	r.Sets = append(r.Sets, s)
	return nil
}

func (r *Recorder) Warn(msg string) { r.Warnings = append(r.Warnings, msg) }
