package dashboard

import (
	"context"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/experiences"
	"github.com/casmith/openshelf/internal/journal"
)

const (
	currentLimit  = 10
	finishedLimit = 6
	activityLimit = 8
	backlogPicks  = 3
)

type currentCard struct {
	db.DashboardCurrentRow
	QuickLog templ.Component
}

type homeData struct {
	Greeting string
	Counters db.DashboardCountersRow
	Current  []currentCard
	Finished []db.DashboardRecentlyFinishedRow
	Activity []journal.Event
	Backlog  []db.DashboardBacklogPicksRow
	Display  db.RatingDisplay
	Loc      *time.Location
}

func load(ctx context.Context, d *deps.Deps, viewer db.User) (homeData, error) {
	loc := journal.Location(viewer)
	now := d.Now().In(loc)
	h := homeData{Greeting: greeting(now), Display: viewer.RatingDisplay, Loc: loc}
	var err error
	if h.Counters, err = d.Q.DashboardCounters(ctx, viewer.ID); err != nil {
		return h, err
	}
	current, err := d.Q.DashboardCurrent(ctx, db.DashboardCurrentParams{UserID: viewer.ID, Lim: currentLimit})
	if err != nil {
		return h, err
	}
	for _, c := range current {
		ql, err := experiences.QuickLog(ctx, d, viewer, c.MediaItem)
		if err != nil {
			return h, err
		}
		h.Current = append(h.Current, currentCard{c, ql})
	}
	if h.Finished, err = d.Q.DashboardRecentlyFinished(ctx, db.DashboardRecentlyFinishedParams{UserID: viewer.ID, Lim: finishedLimit}); err != nil {
		return h, err
	}
	if h.Activity, err = journal.Recent(ctx, d.Q, viewer.ID, activityLimit); err != nil {
		return h, err
	}
	h.Backlog, err = d.Q.DashboardBacklogPicks(ctx, db.DashboardBacklogPicksParams{
		UserID: viewer.ID, Seed: backlogSeed(viewer, now), Lim: backlogPicks,
	})
	return h, err
}

// backlogSeed changes once per local day so the picks are stable within a day.
func backlogSeed(u db.User, localNow time.Time) string {
	return u.PublicID.String() + localNow.Format("2006-01-02")
}

func greeting(localNow time.Time) string {
	switch h := localNow.Hour(); {
	case h >= 5 && h < 12:
		return "Good morning"
	case h >= 12 && h < 18:
		return "Good afternoon"
	}
	return "Good evening"
}

func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func status(c db.DashboardCurrentRow) string {
	if c.State == db.ExperienceStatePaused {
		return "Paused"
	}
	doing := journal.Doing(c.MediaItem.Kind)
	return string(doing[0]-'a'+'A') + doing[1:]
}

// percent is the progress bar fill, or -1 when there's no total to measure against.
func percent(c db.DashboardCurrentRow) int {
	if c.ProgressCurrent == nil {
		return -1
	}
	if c.ProgressUnit != nil && *c.ProgressUnit == db.ProgressUnitPercent {
		return clamp(int(*c.ProgressCurrent))
	}
	if c.ProgressTotal == nil || *c.ProgressTotal <= 0 {
		return -1
	}
	return clamp(int(*c.ProgressCurrent / *c.ProgressTotal * 100))
}

func clamp(n int) int { return max(0, min(100, n)) }
