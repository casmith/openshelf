package synchistory

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

type sourceRow struct {
	db.SynchistoryItemSourcesRow
	ProviderName string
}

type progressRow struct {
	db.SynchistoryItemProgressRow
	ProviderName string
}

type panelView struct {
	Item     db.MediaItem
	Rows     []sourceRow
	Progress []progressRow
	Loc      *time.Location
}

// ItemPanel shows the viewer's synced sources for an item (playtime, listening
// progress, last played, exclude).
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	rows, err := d.Q.SynchistoryItemSources(ctx, db.SynchistoryItemSourcesParams{UserID: viewer.ID, MediaItemID: item.ID})
	if err != nil {
		return nil, err
	}
	progress, err := d.Q.SynchistoryItemProgress(ctx, db.SynchistoryItemProgressParams{UserID: viewer.ID, MediaItemID: item.ID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 && len(progress) == 0 {
		return templ.NopComponent, nil
	}
	v := panelView{Item: item, Loc: location(viewer.Timezone)}
	for _, r := range rows {
		v.Rows = append(v.Rows, sourceRow{SynchistoryItemSourcesRow: r, ProviderName: providerName(d, r.Provider)})
	}
	for _, r := range progress {
		v.Progress = append(v.Progress, progressRow{SynchistoryItemProgressRow: r, ProviderName: providerName(d, r.Provider)})
	}
	return itemPanel(v), nil
}

// progressSummary reads "43% · 5 h 12 m of 12 h · last listened Mar 3, 2026",
// or "Finished on Mar 3, 2026" once the source reports it finished.
func progressSummary(r progressRow, loc *time.Location) string {
	if r.Finished {
		if r.FinishedAt != nil {
			return "Finished on " + r.FinishedAt.In(loc).Format("Jan 2, 2006")
		}
		return "Finished"
	}
	s := strconv.Itoa(percent(r.Fraction)) + "%"
	if r.CurrentSeconds != nil && r.DurationSeconds != nil && *r.DurationSeconds > 0 {
		s += " · " + duration(*r.CurrentSeconds) + " of " + duration(*r.DurationSeconds)
	}
	if r.LastActivityAt != nil {
		s += " · last listened " + r.LastActivityAt.In(loc).Format("Jan 2, 2006")
	}
	return s
}

// percent floors, so 99.6% doesn't read as 100% before the source says finished.
func percent(f float64) int { return int(math.Floor(min(max(f, 0), 1)*100 + 1e-6)) }

func duration(seconds int32) string {
	m := (seconds + 30) / 60
	h, m := m/60, m%60
	switch {
	case h == 0:
		return strconv.Itoa(int(m)) + " m"
	case m == 0:
		return strconv.Itoa(int(h)) + " h"
	}
	return strconv.Itoa(int(h)) + " h " + strconv.Itoa(int(m)) + " m"
}
