package activity

import (
	"context"
	"time"

	"github.com/casmith/openshelf/internal/db"
)

// Event is a journal entry. Set exactly the source ID that the verb refers to.
type Event struct {
	UserID         int64
	Verb           db.ActivityVerb
	MediaItemID    int64
	LibraryEntryID *int64
	ExperienceID   *int64
	ReviewID       *int64
	ListID         *int64
	Visibility     db.Visibility
	OccurredAt     time.Time // zero means now
}

// Record is the only way to write activity_events. Pass a tx-bound Queries
// so the event commits with the change it describes.
func Record(ctx context.Context, q *db.Queries, ev Event) (db.ActivityEvent, error) {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	if ev.Visibility == "" {
		ev.Visibility = db.VisibilityPrivate
	}
	return q.ActivityInsert(ctx, db.ActivityInsertParams{
		UserID:         ev.UserID,
		Verb:           ev.Verb,
		MediaItemID:    ev.MediaItemID,
		LibraryEntryID: ev.LibraryEntryID,
		ExperienceID:   ev.ExperienceID,
		ReviewID:       ev.ReviewID,
		ListID:         ev.ListID,
		Visibility:     ev.Visibility,
		OccurredAt:     ev.OccurredAt,
	})
}

// Source identifies the record whose events change visibility; set exactly one field.
type Source struct {
	LibraryEntryID *int64
	ExperienceID   *int64
	ReviewID       *int64
	ListID         *int64
}

// SetVisibility keeps a record's journal events in step with the record's own visibility.
func SetVisibility(ctx context.Context, q *db.Queries, userID int64, src Source, vis db.Visibility) error {
	return q.ActivitySetVisibility(ctx, db.ActivitySetVisibilityParams{
		Visibility:     vis,
		UserID:         userID,
		ReviewID:       src.ReviewID,
		ListID:         src.ListID,
		ExperienceID:   src.ExperienceID,
		LibraryEntryID: src.LibraryEntryID,
	})
}
