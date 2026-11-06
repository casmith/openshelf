package achievements

import (
	"context"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const (
	BasisEnding     = "ending"
	BasisLastUnlock = "last_unlock"
)

// FinishHint is a suggested finish date drawn from the viewer's unlocks.
type FinishHint struct {
	Day   time.Time // calendar day in the viewer's timezone, as a UTC midnight
	At    time.Time
	Basis string
	Name  string // the ending achievement, for BasisEnding
}

// SuggestedFinishDate dates a finish from the viewer's earliest ending achievement,
// else their latest unlock. It returns nil when the viewer has no dated unlocks.
func SuggestedFinishDate(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (*FinishHint, error) {
	if item.Kind != db.MediaKindGame {
		return nil, nil
	}
	rows, err := d.Q.AchievementsUnlocksForItem(ctx, db.AchievementsUnlocksForItemParams{UserID: viewer.ID, ItemID: item.ID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	h := FinishHint{At: rows[len(rows)-1].UnlockedAt, Basis: BasisLastUnlock}
	for _, r := range rows {
		if Phrase(item.Title, Achievement{Name: r.Name, Description: r.Description}) == Strong {
			h = FinishHint{At: r.UnlockedAt, Basis: BasisEnding, Name: r.Name}
			break
		}
	}
	loc, err := time.LoadLocation(viewer.Timezone)
	if err != nil {
		loc = time.UTC
	}
	y, m, day := h.At.In(loc).Date()
	h.Day = time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	return &h, nil
}
