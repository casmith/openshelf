// Package achievements shows synced achievements on the item page and library (S21).
package achievements

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

func Register(mux *http.ServeMux, d *deps.Deps) {}

type unlock struct {
	Account string
	At      *time.Time
}

type row struct {
	Name, Description, IconURL string
	Hidden                     bool
	Unlocks                    []unlock
}

type panelData struct {
	Unlocked, Total int
	MultiAccount    bool
	Unlocks, Locked []row
	Loc             *time.Location
}

// ItemPanel renders the viewer's achievements for an item on the item page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	rs, err := d.Q.AchievementsForItem(ctx, db.AchievementsForItemParams{UserID: viewer.ID, ItemID: item.ID})
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return templ.NopComponent, nil
	}
	p := panelData{MultiAccount: rs[0].AccountCount > 1, Loc: time.UTC}
	if loc, err := time.LoadLocation(viewer.Timezone); err == nil {
		p.Loc = loc
	}
	var rows []row
	byID := map[int64]int{}
	for _, r := range rs {
		i, ok := byID[r.ID]
		if !ok {
			i = len(rows)
			byID[r.ID] = i
			rows = append(rows, row{Name: r.Name, Description: deref(r.Description), IconURL: deref(r.IconUrl), Hidden: r.Hidden})
		}
		if r.AccountID != nil {
			rows[i].Unlocks = append(rows[i].Unlocks, unlock{Account: r.AccountName, At: r.UnlockedAt})
		}
	}
	for _, r := range rows {
		if len(r.Unlocks) > 0 {
			p.Unlocks = append(p.Unlocks, r)
		} else {
			p.Locked = append(p.Locked, r)
		}
	}
	p.Total, p.Unlocked = len(rows), len(p.Unlocks)
	return panel(p), nil
}

// Count is the viewer's unlocked/total achievements for one item across their linked accounts.
type Count struct {
	Unlocked int
	Total    int
}

// Counts returns achievement counts for items that have any, in one query (library cards call it).
func Counts(ctx context.Context, d *deps.Deps, viewer db.User, itemIDs []int64) (map[int64]Count, error) {
	out := map[int64]Count{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	rs, err := d.Q.AchievementsCounts(ctx, db.AchievementsCountsParams{UserID: viewer.ID, ItemIds: itemIDs})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		out[r.MediaItemID] = Count{Unlocked: int(r.Unlocked), Total: int(r.Total)}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func percent(n, total int) int {
	if total == 0 {
		return 0
	}
	return n * 100 / total
}

func progressWidth(p panelData) string {
	return "width: " + strconv.Itoa(percent(p.Unlocked, p.Total)) + "%"
}

func unlockLabel(u unlock, loc *time.Location, multi bool) string {
	s := ""
	if u.At != nil {
		s = u.At.In(loc).Format("Jan 2, 2006")
	}
	if multi && u.Account != "" {
		if s != "" {
			s += " · "
		}
		s += u.Account
	}
	return s
}
