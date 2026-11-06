package achievements

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const (
	BasisEnding     = "ending"
	BasisLastUnlock = "last_unlock"
	BasisLastPlayed = "last_played"
	// Audiobookshelf bases, for books.
	BasisListenFinished = "listen_finished"
	BasisListenNearly   = "listen_nearly"
)

// listenedMin matches the suggestions threshold for a nearly finished audiobook.
const listenedMin = 0.95

// FinishHint is a suggested finish date drawn from the viewer's unlocks, playtime, or listening.
type FinishHint struct {
	Day   time.Time // calendar day in the viewer's timezone, as a UTC midnight
	At    time.Time
	Basis string
	Name  string // the ending achievement, for BasisEnding
}

// SuggestedFinishDate dates a game's finish from the viewer's earliest ending achievement,
// else their latest unlock, else when they last played; and a book's from its
// Audiobookshelf finish, else its last listen once nearly done. It returns nil when there is no date.
func SuggestedFinishDate(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (*FinishHint, error) {
	var h *FinishHint
	var err error
	switch item.Kind {
	case db.MediaKindGame:
		h, err = gameFinish(ctx, d, viewer, item)
	case db.MediaKindBook:
		h, err = bookFinish(ctx, d, viewer, item)
	}
	if h == nil || err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(viewer.Timezone)
	if err != nil {
		loc = time.UTC
	}
	y, m, day := h.At.In(loc).Date()
	h.Day = time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	return h, nil
}

func bookFinish(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (*FinishHint, error) {
	rows, err := d.Q.AchievementsProgressForItem(ctx, db.AchievementsProgressForItemParams{UserID: viewer.ID, ItemID: item.ID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	switch r := rows[0]; {
	case r.Finished:
		if r.FinishedAt != nil {
			return &FinishHint{At: *r.FinishedAt, Basis: BasisListenFinished}, nil
		}
	case r.Fraction >= listenedMin && r.LastActivityAt != nil:
		return &FinishHint{At: *r.LastActivityAt, Basis: BasisListenNearly}, nil
	}
	return nil, nil
}

func gameFinish(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (*FinishHint, error) {
	rows, err := d.Q.AchievementsUnlocksForItem(ctx, db.AchievementsUnlocksForItemParams{UserID: viewer.ID, ItemID: item.ID})
	if err != nil {
		return nil, err
	}
	var h FinishHint
	if len(rows) == 0 {
		at, err := d.Q.AchievementsLastPlayed(ctx, db.AchievementsLastPlayedParams{UserID: viewer.ID, ItemID: item.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		h = FinishHint{At: at, Basis: BasisLastPlayed}
	} else {
		h = FinishHint{At: rows[len(rows)-1].UnlockedAt, Basis: BasisLastUnlock}
		for _, r := range rows {
			if Phrase(item.Title, Achievement{Name: r.Name, Description: r.Description}) == Strong {
				h = FinishHint{At: r.UnlockedAt, Basis: BasisEnding, Name: r.Name}
				break
			}
		}
	}
	return &h, nil
}
