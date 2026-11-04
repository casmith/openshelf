package journal

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // users.timezone must resolve even without system zoneinfo

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
)

type Event = db.JournalListEventsRow

type Filter struct {
	Kind *db.MediaKind
	Verb *db.ActivityVerb
}

// Cursor is a keyset position: an event's occurred_at and public_id.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

func CursorOf(e Event) *Cursor { return &Cursor{At: e.OccurredAt, ID: e.PublicID} }

func (c Cursor) String() string {
	return strconv.FormatInt(c.At.UnixMicro(), 10) + "_" + c.ID.String()
}

func ParseCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	at, id, ok := strings.Cut(s, "_")
	if !ok {
		return nil, errors.New("bad cursor")
	}
	us, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return nil, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	return &Cursor{At: time.UnixMicro(us).UTC(), ID: u}, nil
}

type Page struct {
	Events []Event
	Newer  *Cursor // pass as ?after=
	Older  *Cursor // pass as ?before=
}

// List returns one page of the viewer's events, newest first. At most one of
// before and after may be set.
func List(ctx context.Context, q *db.Queries, userID int64, f Filter, before, after *Cursor, n int) (Page, error) {
	var p Page
	lim := int32(n + 1)
	if after != nil {
		rows, err := q.JournalListEventsAfter(ctx, db.JournalListEventsAfterParams{
			UserID: userID, Kind: f.Kind, Verb: f.Verb, AfterAt: after.At, AfterID: after.ID, Lim: lim,
		})
		if err != nil {
			return p, err
		}
		more := len(rows) > n
		if more {
			rows = rows[:n]
		}
		for _, r := range slices.Backward(rows) {
			p.Events = append(p.Events, Event(r))
		}
		if len(p.Events) > 0 {
			p.Older = CursorOf(p.Events[len(p.Events)-1])
			if more {
				p.Newer = CursorOf(p.Events[0])
			}
		}
		return p, nil
	}
	params := db.JournalListEventsParams{UserID: userID, Kind: f.Kind, Verb: f.Verb, Lim: lim}
	if before != nil {
		params.BeforeAt, params.BeforeID = &before.At, &before.ID
	}
	rows, err := q.JournalListEvents(ctx, params)
	if err != nil {
		return p, err
	}
	more := len(rows) > n
	if more {
		rows = rows[:n]
	}
	p.Events = rows
	if len(rows) > 0 {
		if more {
			p.Older = CursorOf(rows[len(rows)-1])
		}
		if before != nil {
			p.Newer = CursorOf(rows[0])
		}
	}
	return p, nil
}

// Recent returns the viewer's latest n events.
func Recent(ctx context.Context, q *db.Queries, userID int64, n int) ([]Event, error) {
	p, err := List(ctx, q, userID, Filter{}, nil, nil, n)
	return p.Events, err
}

// Location resolves the user's timezone, falling back to UTC.
func Location(u db.User) *time.Location {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

type Day struct {
	Date   time.Time // local midnight
	Events []Event
}

// GroupByDay splits events (already ordered) into calendar days in loc.
func GroupByDay(events []Event, loc *time.Location) []Day {
	var days []Day
	for _, e := range events {
		t := e.OccurredAt.In(loc)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		if len(days) == 0 || !days[len(days)-1].Date.Equal(d) {
			days = append(days, Day{Date: d})
		}
		days[len(days)-1].Events = append(days[len(days)-1].Events, e)
	}
	return days
}

func DayLabel(day, now time.Time) string {
	now = now.In(day.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, day.Location())
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case day.Year() == now.Year():
		return day.Format("Monday, January 2")
	}
	return day.Format("Monday, January 2, 2006")
}

// Doing is the kind-specific gerund: "playing", "reading", "watching".
func Doing(k db.MediaKind) string {
	switch k {
	case db.MediaKindGame:
		return "playing"
	case db.MediaKindBook:
		return "reading"
	}
	return "watching"
}

// Sentence splits an event into text around the item title.
func Sentence(e Event) (before, after string) {
	switch e.Verb {
	case db.ActivityVerbAdded:
		return "Added", "to your library"
	case db.ActivityVerbStarted:
		return "Started " + Doing(e.Kind), ""
	case db.ActivityVerbPaused:
		return "Paused", ""
	case db.ActivityVerbResumed:
		return "Resumed", ""
	case db.ActivityVerbProgressed:
		return "Made progress on", ""
	case db.ActivityVerbFinished:
		return "Finished", ""
	case db.ActivityVerbAbandoned:
		return "Stopped " + Doing(e.Kind), ""
	case db.ActivityVerbRated:
		return "Rated", ""
	case db.ActivityVerbReviewed:
		return "Reviewed", ""
	case db.ActivityVerbListed:
		if e.ListName == nil {
			return "Added", "to a list"
		}
		return "Added", "to"
	}
	return string(e.Verb), ""
}

// Progress formats a progress value like "1079 / 1496 pages" or "40%".
func Progress(unit *db.ProgressUnit, cur, total *float64) string {
	if unit == nil || cur == nil {
		return ""
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	if *unit == db.ProgressUnitPercent {
		return num(*cur) + "%"
	}
	s := num(*cur)
	if total != nil {
		s += " / " + num(*total)
	}
	return s + " " + string(*unit)
}

func ItemHref(id uuid.UUID) string { return "/items/" + id.String() }
