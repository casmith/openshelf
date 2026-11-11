package experiences

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/achievements"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web/ui"
)

type panelData struct {
	Item   db.MediaItem
	Exps   []db.Experience
	Today  string
	Error  string
	Hint   *achievements.FinishHint // prefills "Log as finished"
	Synced *synced
}

// synced is what linked accounts report for an item: Steam playtime or
// Audiobookshelf progress. It replaces manual progress entry.
type synced struct {
	Source   string
	Progress bool // fraction-based progress; otherwise playtime minutes
	Minutes  int32
	Fraction float64
	Finished bool
}

func newSynced(d *deps.Deps, minutes int32, ptProvider string, fraction float64, finished bool, prProvider string) *synced {
	switch {
	case prProvider != "":
		return &synced{Source: sourceName(d, prProvider), Progress: true, Fraction: min(max(fraction, 0), 1), Finished: finished}
	case ptProvider != "":
		return &synced{Source: sourceName(d, ptProvider), Minutes: minutes}
	}
	return nil
}

func loadSynced(ctx context.Context, d *deps.Deps, viewer db.User, itemID int64) (*synced, error) {
	rows, err := d.Q.ExperiencesSynced(ctx, db.ExperiencesSyncedParams{UserID: viewer.ID, MediaItemIds: []int64{itemID}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	r := rows[0]
	return newSynced(d, r.PlaytimeMinutes, r.PlaytimeProvider, r.ProgressFraction, r.ProgressFinished, r.ProgressProvider), nil
}

func sourceName(d *deps.Deps, provider string) string {
	if a, ok := d.SyncAdapters.Get(provider); ok {
		return a.Capabilities().DisplayName
	}
	return provider
}

// usesSynced reports whether e's progress comes from a linked account instead of manual entry.
// Playtime only stands in for hours; fraction progress stands in for any unit.
func usesSynced(e db.Experience, s *synced) bool {
	return s != nil && e.ProgressUnit != nil && (s.Progress || *e.ProgressUnit == db.ProgressUnitHours)
}

func manualProgress(e db.Experience, s *synced) bool {
	return e.ProgressUnit != nil && !usesSynced(e, s)
}

func syncedText(s *synced) string {
	switch {
	case s.Progress && s.Finished:
		return "Finished on " + s.Source
	case s.Progress && s.Fraction == 0:
		return "Not started on " + s.Source
	case s.Progress:
		return strconv.Itoa(max(int(math.Round(s.Fraction*100)), 1)) + "% complete on " + s.Source
	case s.Minutes == 0:
		return "Not played yet on " + s.Source
	}
	return ui.Hours(s.Minutes) + " played on " + s.Source
}

// ItemPanel renders the viewer's experiences of item for the item detail page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	return loadPanel(ctx, d, viewer, item, "")
}

func loadPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem, errMsg string) (templ.Component, error) {
	exps, err := d.Q.ExperiencesListForItem(ctx, db.ExperiencesListForItemParams{UserID: viewer.ID, MediaItemID: item.ID})
	if err != nil {
		return nil, err
	}
	today := Today(d.Now(), viewer.Timezone)
	p := panelData{Item: item, Exps: exps, Today: today.Format(time.DateOnly), Error: errMsg}
	if p.Synced, err = loadSynced(ctx, d, viewer, item.ID); err != nil {
		return nil, err
	}
	if _, busy := InProgress(exps); !busy {
		if p.Hint, err = achievements.SuggestedFinishDate(ctx, d, viewer, item); err != nil {
			return nil, err
		}
		if p.Hint != nil && p.Hint.Day.After(today) {
			p.Hint = nil
		}
	}
	return panel(p), nil
}

// QuickLog renders an inline progress/finish control for dashboard cards (used by S06).
func QuickLog(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	m, err := QuickLogs(ctx, d, viewer, []db.MediaItem{item})
	if err != nil {
		return nil, err
	}
	return m[item.ID], nil
}

// QuickLogs is QuickLog for many items in one query, keyed by media item ID.
func QuickLogs(ctx context.Context, d *deps.Deps, viewer db.User, items []db.MediaItem) (map[int64]templ.Component, error) {
	ids := make([]int64, len(items))
	byID := make(map[int64]db.MediaItem, len(items))
	out := make(map[int64]templ.Component, len(items))
	for i, it := range items {
		ids[i], byID[it.ID], out[it.ID] = it.ID, it, templ.NopComponent
	}
	es, err := d.Q.ExperiencesActiveForItems(ctx, db.ExperiencesActiveForItemsParams{UserID: viewer.ID, MediaItemIds: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range es {
		s := newSynced(d, r.PlaytimeMinutes, r.PlaytimeProvider, r.ProgressFraction, r.ProgressFinished, r.ProgressProvider)
		out[r.Experience.MediaItemID] = quickLog(byID[r.Experience.MediaItemID], r.Experience, s)
	}
	return out, nil
}

var rowActions = []Action{ActionStart, ActionPause, ActionResume, ActionFinish, ActionAbandon}

func itemURL(item db.MediaItem) string   { return "/items/" + item.PublicID.String() }
func createURL(item db.MediaItem) string { return itemURL(item) + "/experiences" }
func quickID(item db.MediaItem) string   { return "quicklog-" + item.PublicID.String() }

func expURL(e db.Experience, suffix string) string {
	u := "/experiences/" + e.PublicID.String()
	if suffix != "" {
		u += "/" + suffix
	}
	return u
}

func active(e db.Experience) bool {
	return e.State == db.ExperienceStateActive || e.State == db.ExperienceStatePaused
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fmtDate(t *time.Time) string { return t.Format("Jan 2, 2006") }

func hintText(h *achievements.FinishHint) string {
	switch h.Basis {
	case achievements.BasisEnding:
		return "From your Steam achievements: ‘" + h.Name + "’ unlocked " + fmtDate(&h.Day)
	case achievements.BasisListenFinished:
		return "From Audiobookshelf (finished " + fmtDate(&h.Day) + ")"
	case achievements.BasisListenNearly:
		return "From when you last listened on Audiobookshelf (" + fmtDate(&h.Day) + ") — clear it if that's not right."
	case achievements.BasisLastPlayed:
		return "From when you last played on Steam (" + fmtDate(&h.Day) + ") — clear it if that's not right."
	}
	return "From your last Steam achievement, unlocked " + fmtDate(&h.Day) + " — clear it if that's not right."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func stateLabel(s db.ExperienceState) string {
	switch s {
	case db.ExperienceStateActive:
		return "In progress"
	case db.ExperienceStatePaused:
		return "Paused"
	case db.ExperienceStateFinished:
		return "Finished"
	case db.ExperienceStateAbandoned:
		return "Abandoned"
	}
	return string(s)
}

func finishedLabel(s db.ExperienceState) string {
	if s == db.ExperienceStateAbandoned {
		return "Abandoned on"
	}
	return "Finished on"
}

func dateLine(e db.Experience) string {
	var parts []string
	if e.StartedOn != nil {
		parts = append(parts, "Started "+fmtDate(e.StartedOn))
	}
	if e.FinishedOn != nil {
		verb := "Finished "
		if e.State == db.ExperienceStateAbandoned {
			verb = "Abandoned "
		}
		parts = append(parts, verb+fmtDate(e.FinishedOn))
	}
	return strings.Join(parts, " · ")
}

func actionLabel(a Action) string {
	if a == ActionStart {
		return "Set start date"
	}
	return capitalize(string(a))
}

func actionVariant(a Action) string {
	switch a {
	case ActionFinish:
		return "primary"
	case ActionAbandon:
		return "ghost"
	}
	return "secondary"
}

func pastVerb(k db.MediaKind) string {
	switch k {
	case db.MediaKindBook:
		return "read"
	case db.MediaKindGame:
		return "finished"
	}
	return "watched"
}

func startLabel(k db.MediaKind, revisit bool) string {
	if revisit {
		return "Revisit"
	}
	switch k {
	case db.MediaKindBook:
		return "Start reading"
	case db.MediaKindGame:
		return "Start playing"
	}
	return "Start watching"
}

// timesSummary renders "Watched twice", "Read once", and so on.
func timesSummary(k db.MediaKind, n int) string {
	var times string
	switch n {
	case 0:
		return ""
	case 1:
		times = "once"
	case 2:
		times = "twice"
	default:
		times = strconv.Itoa(n) + " times"
	}
	return capitalize(pastVerb(k)) + " " + times
}

func unitLabel(u db.ProgressUnit) string {
	if u == db.ProgressUnitPercent {
		return "%"
	}
	return string(u)
}

func progressText(e db.Experience) string {
	s := fmtNum(deref(e.ProgressCurrent))
	if e.ProgressTotal != nil {
		s += " / " + fmtNum(*e.ProgressTotal)
	}
	if *e.ProgressUnit == db.ProgressUnitPercent {
		return s + "%"
	}
	return s + " " + string(*e.ProgressUnit)
}

func unitOptions() []ui.Option {
	opts := []ui.Option{{Value: "", Label: "None"}}
	for _, u := range db.AllProgressUnitValues() {
		opts = append(opts, ui.Option{Value: string(u), Label: capitalize(string(u))})
	}
	return opts
}

func visibilityOptions() []ui.Option {
	return []ui.Option{
		{Value: string(db.VisibilityPrivate), Label: "Private"},
		{Value: string(db.VisibilityInstance), Label: "Instance (logged-in users)"},
		{Value: string(db.VisibilityPublic), Label: "Public"},
	}
}
