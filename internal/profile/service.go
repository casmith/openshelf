package profile

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/journal"
)

const (
	inProgressLimit = 48
	completedLimit  = 60
	backlogLimit    = 24
	stalledLimit    = 20
	reviewsLimit    = 5
	listsLimit      = 50
)

// cursor is the last completion shown: a dated group (Day set) or an undated one (At set).
type cursor struct {
	Day *time.Time
	At  time.Time
	ID  uuid.UUID
}

func (c cursor) String() string {
	if c.Day == nil {
		return "u" + strconv.FormatInt(c.At.UnixMicro(), 10) + "_" + c.ID.String()
	}
	return c.Day.Format(time.DateOnly) + "_" + c.ID.String()
}

func parseCursor(s string) (*cursor, error) {
	key, id, ok := strings.Cut(s, "_")
	if !ok {
		return nil, errors.New("bad cursor")
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	if micros, ok := strings.CutPrefix(key, "u"); ok {
		n, err := strconv.ParseInt(micros, 10, 64)
		if err != nil {
			return nil, err
		}
		return &cursor{At: time.UnixMicro(n), ID: u}, nil
	}
	d, err := time.Parse(time.DateOnly, key)
	if err != nil {
		return nil, err
	}
	return &cursor{Day: &d, ID: u}, nil
}

// yearGroup is one year of dated completions, or the undated "Earlier" group when Year is 0.
type yearGroup struct {
	Year  int
	Items []db.ProfileCompletedRow
}

func (g yearGroup) Label() string {
	if g.Year == 0 {
		return "Earlier"
	}
	return strconv.Itoa(g.Year)
}

func rowYear(r db.ProfileCompletedRow) int {
	if r.LastDay == nil {
		return 0
	}
	return r.LastDay.Year()
}

func (c cursor) year() int {
	if c.Day == nil {
		return 0
	}
	return c.Day.Year()
}

type completed struct {
	Years []yearGroup
	// ContinuesYear is set when the first group continues a group shown on the previous page.
	ContinuesYear bool
	Next          *cursor
}

type profileData struct {
	Owner      db.User
	Counts     db.ProfileGetUserRow
	Own        bool
	Kind       *db.MediaKind
	Display    db.RatingDisplay
	InProgress []db.ProfileInProgressRow
	Completed  completed
	Backlog    []db.ProfileBacklogRow
	Stalled    []db.ProfileStalledRow
	Reviews    []db.ProfileReviewsRow
	Lists      []db.ProfileListsRow
}

func (p profileData) empty() bool {
	return len(p.InProgress) == 0 && len(p.Completed.Years) == 0 && len(p.Backlog) == 0 &&
		len(p.Stalled) == 0 && len(p.Reviews) == 0 && len(p.Lists) == 0
}

func (p profileData) href(kind *db.MediaKind, before *cursor) string {
	v := url.Values{}
	if kind != nil {
		v.Set("kind", string(*kind))
	}
	if before != nil {
		v.Set("before", before.String())
	}
	h := "/@" + p.Owner.Username
	if len(v) > 0 {
		h += "?" + v.Encode()
	}
	return h
}

func loadUser(ctx context.Context, d *deps.Deps, username string, viewer db.User, kind *db.MediaKind) (profileData, error) {
	row, err := d.Q.ProfileGetUser(ctx, db.ProfileGetUserParams{ViewerID: viewer.ID, Kind: kind, Username: username})
	if err != nil {
		return profileData{}, err
	}
	return profileData{
		Owner: row.User, Counts: row, Own: row.User.ID == viewer.ID, Kind: kind, Display: viewer.RatingDisplay,
	}, nil
}

func loadCompleted(ctx context.Context, d *deps.Deps, p profileData, viewer db.User, before *cursor) (completed, error) {
	arg := db.ProfileCompletedParams{
		OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: completedLimit + 1,
	}
	if before != nil {
		arg.BeforeDay, arg.BeforeID = before.Day, &before.ID
		if before.Day == nil {
			arg.BeforeAt = &before.At
		}
	}
	rows, err := d.Q.ProfileCompleted(ctx, arg)
	if err != nil {
		return completed{}, err
	}
	var c completed
	if len(rows) > completedLimit {
		rows = rows[:completedLimit]
		last := rows[len(rows)-1]
		c.Next = &cursor{Day: last.LastDay, At: last.LastAt, ID: last.PublicID}
	}
	for _, r := range rows {
		if n := len(c.Years); n == 0 || c.Years[n-1].Year != rowYear(r) {
			c.Years = append(c.Years, yearGroup{Year: rowYear(r)})
		}
		c.Years[len(c.Years)-1].Items = append(c.Years[len(c.Years)-1].Items, r)
	}
	c.ContinuesYear = before != nil && len(c.Years) > 0 && c.Years[0].Year == before.year()
	return c, nil
}

func load(ctx context.Context, d *deps.Deps, p profileData, viewer db.User, before *cursor) (profileData, error) {
	var err error
	tz := journal.Location(p.Owner).String()
	if p.InProgress, err = d.Q.ProfileInProgress(ctx, db.ProfileInProgressParams{
		OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: inProgressLimit,
	}); err != nil {
		return p, err
	}
	if p.Completed, err = loadCompleted(ctx, d, p, viewer, before); err != nil {
		return p, err
	}
	if p.Backlog, err = d.Q.ProfileBacklog(ctx, db.ProfileBacklogParams{
		OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: backlogLimit,
	}); err != nil {
		return p, err
	}
	if p.Stalled, err = d.Q.ProfileStalled(ctx, db.ProfileStalledParams{
		Tz: tz, OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: stalledLimit,
	}); err != nil {
		return p, err
	}
	if p.Reviews, err = d.Q.ProfileReviews(ctx, db.ProfileReviewsParams{
		OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: reviewsLimit,
	}); err != nil {
		return p, err
	}
	p.Lists, err = d.Q.ProfileLists(ctx, db.ProfileListsParams{
		OwnerID: p.Owner.ID, ViewerID: viewer.ID, Kind: p.Kind, Lim: listsLimit,
	})
	return p, err
}

// progress formats compact progress like "p. 1079 / 1496", "Ep 30 / 62", or "64 h".
func progress(unit *db.ProgressUnit, cur, total *float64) string {
	if unit == nil || cur == nil {
		return ""
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	frac := num(*cur)
	if total != nil && *total > 0 {
		frac += " / " + num(*total)
	}
	switch *unit {
	case db.ProgressUnitPercent:
		return num(*cur) + "%"
	case db.ProgressUnitPages:
		return "p. " + frac
	case db.ProgressUnitChapters:
		return "Ch. " + frac
	case db.ProgressUnitEpisodes:
		return "Ep " + frac
	case db.ProgressUnitHours:
		return frac + " h"
	case db.ProgressUnitMinutes:
		return frac + " min"
	}
	return frac
}

func stalledDay(s db.ProfileStalledRow) *time.Time {
	if s.State == db.ExperienceStatePaused {
		return &s.PausedOn
	}
	return s.FinishedOn
}

func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return count(n) + " " + many
}
