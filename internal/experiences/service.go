package experiences

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
)

var (
	ErrInvalidTransition = errors.New("experiences: transition not allowed from the current state")
	ErrNoProgress        = errors.New("experiences: experience does not track progress")
	ErrBadProgress       = errors.New("experiences: progress must be zero or more")
)

type Action string

const (
	ActionStart   Action = "start"
	ActionPause   Action = "pause"
	ActionResume  Action = "resume"
	ActionFinish  Action = "finish"
	ActionAbandon Action = "abandon"
)

type transition struct {
	from []db.ExperienceState
	to   db.ExperienceState
	verb db.ActivityVerb
}

// "start" only applies to an active experience without a start date (for example
// one whose date was cleared); revisiting creates a new experience instead.
var transitions = map[Action]transition{
	ActionStart:   {[]db.ExperienceState{db.ExperienceStateActive}, db.ExperienceStateActive, db.ActivityVerbStarted},
	ActionPause:   {[]db.ExperienceState{db.ExperienceStateActive}, db.ExperienceStatePaused, db.ActivityVerbPaused},
	ActionResume:  {[]db.ExperienceState{db.ExperienceStatePaused}, db.ExperienceStateActive, db.ActivityVerbResumed},
	ActionFinish:  {[]db.ExperienceState{db.ExperienceStateActive, db.ExperienceStatePaused}, db.ExperienceStateFinished, db.ActivityVerbFinished},
	ActionAbandon: {[]db.ExperienceState{db.ExperienceStateActive, db.ExperienceStatePaused}, db.ExperienceStateAbandoned, db.ActivityVerbAbandoned},
}

// Allowed reports whether a can be applied to e.
func Allowed(e db.Experience, a Action) bool {
	t, ok := transitions[a]
	if !ok {
		return false
	}
	if a == ActionStart && e.StartedOn != nil {
		return false
	}
	for _, s := range t.from {
		if e.State == s {
			return true
		}
	}
	return false
}

// Today is the current calendar date in the user's timezone, as a UTC midnight.
func Today(now time.Time, tz string) time.Time {
	if loc, err := time.LoadLocation(tz); err == nil {
		now = now.In(loc)
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// DefaultUnit picks the progress unit for a new experience of kind.
func DefaultUnit(kind db.MediaKind) *db.ProgressUnit {
	var u db.ProgressUnit
	switch kind {
	case db.MediaKindBook:
		u = db.ProgressUnitPages
	case db.MediaKindShow:
		u = db.ProgressUnitEpisodes
	case db.MediaKindGame:
		u = db.ProgressUnitHours
	default:
		return nil
	}
	return &u
}

func record(ctx context.Context, q *db.Queries, e db.Experience, verb db.ActivityVerb, at time.Time) error {
	_, err := activity.Record(ctx, q, activity.Event{
		UserID:       e.UserID,
		Verb:         verb,
		MediaItemID:  e.MediaItemID,
		ExperienceID: &e.ID,
		Visibility:   e.Visibility,
		OccurredAt:   at,
	})
	return err
}

type CreateInput struct {
	Finished   bool       // log a completed experience instead of starting one
	Edition    *uuid.UUID // public ID of one of the item's editions
	StartedOn  *time.Time
	FinishedOn *time.Time
}

// Create starts a new experience (also used for revisits) or logs a finished one.
func Create(ctx context.Context, q *db.Queries, now time.Time, viewer db.User, item db.MediaItem, in CreateInput) (db.Experience, error) {
	today := Today(now, viewer.Timezone)
	eds, err := q.ExperiencesItemEditions(ctx, item.ID)
	if err != nil {
		return db.Experience{}, err
	}
	editionID, err := resolveEdition(ctx, q, viewer, item, eds, in.Edition)
	if err != nil {
		return db.Experience{}, err
	}
	p := db.ExperiencesInsertParams{
		UserID:       viewer.ID,
		MediaItemID:  item.ID,
		EditionID:    editionID,
		State:        db.ExperienceStateActive,
		StartedOn:    in.StartedOn,
		ProgressUnit: DefaultUnit(item.Kind),
		Visibility:   viewer.DefaultVisibility,
	}
	if p.ProgressUnit != nil {
		if p.ProgressTotal, err = defaultTotal(ctx, q, item, *p.ProgressUnit, eds, editionID); err != nil {
			return db.Experience{}, err
		}
	}
	verb := db.ActivityVerbStarted
	if in.Finished {
		verb = db.ActivityVerbFinished
		p.State = db.ExperienceStateFinished
		p.FinishedOn = in.FinishedOn
		if p.FinishedOn == nil {
			p.FinishedOn = &today
		}
		p.ProgressCurrent = p.ProgressTotal
	} else if p.StartedOn == nil {
		p.StartedOn = &today
	}
	if p.ProgressUnit != nil && p.ProgressCurrent == nil {
		zero := 0.0
		p.ProgressCurrent = &zero
	}
	e, err := q.ExperiencesInsert(ctx, p)
	if err != nil {
		return db.Experience{}, err
	}
	return e, record(ctx, q, e, verb, now)
}

// resolveEdition uses the requested edition if it belongs to the item, else the viewer's library edition.
func resolveEdition(ctx context.Context, q *db.Queries, viewer db.User, item db.MediaItem, eds []db.ExperiencesItemEditionsRow, want *uuid.UUID) (*int64, error) {
	if want != nil {
		for _, e := range eds {
			if e.PublicID == *want {
				return &e.ID, nil
			}
		}
	}
	if len(eds) == 1 {
		return &eds[0].ID, nil
	}
	id, err := q.ExperiencesLibraryEditionID(ctx, db.ExperiencesLibraryEditionIDParams{UserID: viewer.ID, MediaItemID: item.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, err
}

func defaultTotal(ctx context.Context, q *db.Queries, item db.MediaItem, unit db.ProgressUnit, eds []db.ExperiencesItemEditionsRow, editionID *int64) (*float64, error) {
	switch unit {
	case db.ProgressUnitPercent:
		v := 100.0
		return &v, nil
	case db.ProgressUnitPages:
		var pages *int32
		for _, e := range eds {
			if e.PageCount == nil {
				continue
			}
			if editionID != nil && e.ID == *editionID {
				pages = e.PageCount
				break
			}
			if pages == nil {
				pages = e.PageCount
			}
		}
		if pages != nil {
			v := float64(*pages)
			return &v, nil
		}
	case db.ProgressUnitEpisodes:
		n, err := q.ExperiencesShowEpisodeCount(ctx, item.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil || n == nil {
			return nil, err
		}
		v := float64(*n)
		return &v, nil
	}
	return nil, nil
}

// Transition applies a state change to the viewer's experience.
func Transition(ctx context.Context, q *db.Queries, now time.Time, viewer db.User, id uuid.UUID, a Action) (db.Experience, error) {
	e, err := q.ExperiencesGetForUpdate(ctx, db.ExperiencesGetForUpdateParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		return e, err
	}
	if !Allowed(e, a) {
		return e, ErrInvalidTransition
	}
	t := transitions[a]
	today := Today(now, viewer.Timezone)
	p := db.ExperiencesUpdateStateParams{
		ID: e.ID, UserID: viewer.ID, State: t.to,
		StartedOn: e.StartedOn, FinishedOn: e.FinishedOn, ProgressCurrent: e.ProgressCurrent,
		UpdatedAt: now,
	}
	switch a {
	case ActionStart:
		p.StartedOn = &today
	case ActionFinish:
		p.FinishedOn = &today
		if e.ProgressTotal != nil {
			p.ProgressCurrent = e.ProgressTotal
		}
	case ActionAbandon:
		p.FinishedOn = &today
	}
	e, err = q.ExperiencesUpdateState(ctx, p)
	if err != nil {
		return e, err
	}
	return e, record(ctx, q, e, t.verb, now)
}

// LogProgress moves progress_current, clamped to the total, and appends a progress event.
func LogProgress(ctx context.Context, q *db.Queries, now time.Time, viewer db.User, id uuid.UUID, value float64) (db.Experience, error) {
	e, err := q.ExperiencesGetForUpdate(ctx, db.ExperiencesGetForUpdateParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		return e, err
	}
	if e.ProgressUnit == nil {
		return e, ErrNoProgress
	}
	if e.State != db.ExperienceStateActive && e.State != db.ExperienceStatePaused {
		return e, ErrInvalidTransition
	}
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return e, ErrBadProgress
	}
	value = clamp(value, e.ProgressTotal)
	if _, err := q.ExperiencesInsertProgressEvent(ctx, db.ExperiencesInsertProgressEventParams{
		OccurredAt: now, Value: value, ExperienceID: e.ID, UserID: viewer.ID,
	}); err != nil {
		return e, err
	}
	e, err = q.ExperiencesUpdateProgress(ctx, db.ExperiencesUpdateProgressParams{
		ID: e.ID, UserID: viewer.ID, ProgressCurrent: &value, UpdatedAt: now,
	})
	if err != nil {
		return e, err
	}
	return e, record(ctx, q, e, db.ActivityVerbProgressed, now)
}

func clamp(v float64, total *float64) float64 {
	v = math.Round(v*100) / 100
	if total != nil && v > *total {
		return *total
	}
	return v
}

type EditInput struct {
	StartedOn     *time.Time
	FinishedOn    *time.Time
	ProgressUnit  *db.ProgressUnit
	ProgressTotal *float64
	Notes         *string
	Visibility    db.Visibility
}

// Edit updates dates and details; state changes go through Transition.
func Edit(ctx context.Context, q *db.Queries, now time.Time, viewer db.User, e db.Experience, in EditInput) (db.Experience, error) {
	total := in.ProgressTotal
	if in.ProgressUnit != nil && *in.ProgressUnit == db.ProgressUnitPercent && total == nil {
		v := 100.0
		total = &v
	}
	current := e.ProgressCurrent
	switch {
	case in.ProgressUnit == nil:
		current, total = nil, nil
	case current == nil:
		zero := 0.0
		current = &zero
	case e.ProgressUnit == nil || *e.ProgressUnit != *in.ProgressUnit:
		zero := 0.0
		current = &zero
	}
	if current != nil {
		v := clamp(*current, total)
		current = &v
	}
	return q.ExperiencesUpdateDetails(ctx, db.ExperiencesUpdateDetailsParams{
		ID: e.ID, UserID: viewer.ID,
		StartedOn: in.StartedOn, FinishedOn: in.FinishedOn,
		ProgressUnit: in.ProgressUnit, ProgressCurrent: current, ProgressTotal: total,
		Notes: in.Notes, Visibility: in.Visibility, UpdatedAt: now,
	})
}

// Delete removes the viewer's experience and returns its item ID.
func Delete(ctx context.Context, q *db.Queries, viewer db.User, id uuid.UUID) (int64, error) {
	return q.ExperiencesDelete(ctx, db.ExperiencesDeleteParams{PublicID: id, UserID: viewer.ID})
}

// TimesFinished counts finished experiences.
func TimesFinished(es []db.Experience) int {
	n := 0
	for _, e := range es {
		if e.State == db.ExperienceStateFinished {
			n++
		}
	}
	return n
}

// InProgress returns the first active or paused experience.
func InProgress(es []db.Experience) (db.Experience, bool) {
	for _, e := range es {
		if e.State == db.ExperienceStateActive || e.State == db.ExperienceStatePaused {
			return e, true
		}
	}
	return db.Experience{}, false
}
