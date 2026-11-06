// Package suggestions proposes games the viewer has probably finished, based on
// synced achievements and playtime. It only suggests: every finish is an explicit user action (ADR 0002).
package suggestions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/achievements"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/experiences"
)

type Suggestion struct {
	ItemID       int64
	PublicID     uuid.UUID
	Title        string
	Kind         db.MediaKind
	CoverImageID *int64
	achievements.Verdict
	Unlocked int
	Total    int
	Minutes  int
	// Normally is the main-story estimate behind a Maybe suggestion.
	Normally int
}

// Maybe is a playtime-only suggestion: no achievement signal, but the viewer has
// played at least as long as most players take to finish.
const Maybe = achievements.Level(-1)

func load(ctx context.Context, q *db.Queries, viewer db.User, itemID *int64) ([]Suggestion, error) {
	rows, err := q.SuggestionsCandidates(ctx, db.SuggestionsCandidatesParams{UserID: viewer.ID, ItemID: itemID})
	if err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, r := range rows {
		g := achievements.Game{Title: r.Title, Unlocked: int(r.Unlocked), Total: int(r.Total)}
		if err := json.Unmarshal(r.Latest, &g.Latest); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(r.Candidates, &g.Candidates); err != nil {
			return nil, err
		}
		v := achievements.Classify(g)
		s := Suggestion{
			ItemID: r.ID, PublicID: r.PublicID, Title: r.Title, Kind: r.Kind, CoverImageID: r.CoverImageID,
			Unlocked: g.Unlocked, Total: g.Total, Minutes: int(r.Minutes),
		}
		switch {
		case v.Level != achievements.None:
			s.Verdict = v
		case r.MaybeNormallyMinutes != nil:
			s.Verdict = achievements.Verdict{Level: Maybe, FinishAt: r.LastPlayedAt}
			s.Normally = int(*r.MaybeNormallyMinutes)
		default:
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// Groups are the viewer's suggestions split by strength.
type Groups struct {
	Strong, Medium, Maybe []Suggestion
}

func List(ctx context.Context, q *db.Queries, viewer db.User) (Groups, error) {
	all, err := load(ctx, q, viewer, nil)
	var g Groups
	for _, s := range all {
		switch s.Level {
		case achievements.Strong:
			g.Strong = append(g.Strong, s)
		case achievements.Medium:
			g.Medium = append(g.Medium, s)
		default:
			g.Maybe = append(g.Maybe, s)
		}
	}
	return g, err
}

// Count is the number of current suggestions, in one query.
func Count(ctx context.Context, d *deps.Deps, viewer db.User) (int, error) {
	all, err := load(ctx, d.Q, viewer, nil)
	return len(all), err
}

// For returns the suggestion for one item, or pgx.ErrNoRows when it isn't one.
func For(ctx context.Context, q *db.Queries, viewer db.User, itemID int64) (Suggestion, error) {
	all, err := load(ctx, q, viewer, &itemID)
	if err != nil {
		return Suggestion{}, err
	}
	if len(all) == 0 {
		return Suggestion{}, pgx.ErrNoRows
	}
	return all[0], nil
}

// FinishDay is the suggested finish date in the viewer's timezone, as a UTC midnight.
func FinishDay(s Suggestion, loc *time.Location) *time.Time {
	if s.FinishAt == nil {
		return nil
	}
	y, m, d := s.FinishAt.In(loc).Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &day
}

// MarkFinished logs a finished experience through the experiences service, dated
// from the verdict unless undated is set.
func MarkFinished(ctx context.Context, q *db.Queries, now time.Time, viewer db.User, s Suggestion, undated bool) error {
	item, err := q.GetMediaItemByID(ctx, s.ItemID)
	if err != nil {
		return err
	}
	in := experiences.CreateInput{Finished: true}
	if !undated {
		in.FinishedOn = FinishDay(s, location(viewer))
	}
	_, err = experiences.Create(ctx, q, now, viewer, item, in)
	return err
}

func location(u db.User) *time.Location {
	if loc, err := time.LoadLocation(u.Timezone); err == nil {
		return loc
	}
	return time.UTC
}
