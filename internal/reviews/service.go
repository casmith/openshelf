package reviews

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
)

var (
	ErrEmpty              = errors.New("add a rating or a review")
	ErrExperienceNotFound = errors.New("experience not found")
)

// SaveInput is a review as submitted. Body is plain text; empty means none.
type SaveInput struct {
	Rating       *int16
	Body         string
	Spoilers     bool
	Visibility   db.Visibility
	ExperienceID *uuid.UUID
}

// Save creates or replaces userID's single review of itemID and records
// rated/reviewed activity. q must be bound to a transaction.
func Save(ctx context.Context, q *db.Queries, userID, itemID int64, in SaveInput, now time.Time) (db.Review, error) {
	body := normalizeBody(in.Body)
	if in.Rating == nil && body == nil {
		return db.Review{}, ErrEmpty
	}
	if in.Rating != nil && (*in.Rating < 0 || *in.Rating > 100) {
		return db.Review{}, ErrRatingRange
	}
	var expID *int64
	if in.ExperienceID != nil {
		id, err := q.ReviewsGetOwnExperienceID(ctx, db.ReviewsGetOwnExperienceIDParams{PublicID: *in.ExperienceID, UserID: userID, MediaItemID: itemID})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Review{}, ErrExperienceNotFound
		}
		if err != nil {
			return db.Review{}, err
		}
		expID = &id
	}

	prev, err := q.ReviewsGetOwnForItem(ctx, db.ReviewsGetOwnForItemParams{UserID: userID, MediaItemID: itemID})
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return db.Review{}, err
	}

	rev, err := q.ReviewsUpsert(ctx, db.ReviewsUpsertParams{
		UserID:           userID,
		MediaItemID:      itemID,
		ExperienceID:     expID,
		Rating:           in.Rating,
		Body:             body,
		ContainsSpoilers: in.Spoilers,
		Visibility:       in.Visibility,
	})
	if err != nil {
		return db.Review{}, err
	}
	if !isNew && prev.Visibility != rev.Visibility {
		if err := q.ReviewsSyncActivityVisibility(ctx, db.ReviewsSyncActivityVisibilityParams{Visibility: rev.Visibility, ReviewID: &rev.ID, UserID: userID}); err != nil {
			return db.Review{}, err
		}
	}

	var verb db.ActivityVerb
	switch {
	case body != nil && (isNew || prev.Body == nil):
		verb = db.ActivityVerbReviewed
	case in.Rating != nil && (isNew || prev.Rating == nil || *prev.Rating != *in.Rating):
		verb = db.ActivityVerbRated
	}
	if verb != "" {
		if _, err := activity.Record(ctx, q, activity.Event{
			UserID:      userID,
			Verb:        verb,
			MediaItemID: itemID,
			ReviewID:    &rev.ID,
			Visibility:  rev.Visibility,
			OccurredAt:  now,
		}); err != nil {
			return db.Review{}, err
		}
	}
	return rev, nil
}

func normalizeBody(s string) *string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return nil
	}
	return &s
}

// Paragraphs splits a plain-text body on blank lines.
func Paragraphs(body *string) []string {
	if body == nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(*body, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
