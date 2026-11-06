// Package connections serves /settings/connections: the user's linked accounts
// and the adapters they can link, with "Sync now".
package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/web"
)

func Register(mux *http.ServeMux, d *deps.Deps) {
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) {
		mux.Handle(pattern, auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) })))
	}
	handle("GET /settings/connections", show)
	handle("POST /settings/connections/{id}/sync", syncNow)
}

type adapterView struct {
	Provider   string
	Caps       sync.Capabilities
	Configured bool
}

type accountView struct {
	db.ConnectionsListForUserRow
	ProviderName string
	Summary      string // "142 books in your library, 4 with progress"
}

type pageData struct {
	Adapters []adapterView
	Accounts []accountView
	Loc      *time.Location
}

func show(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	viewer, _ := auth.CurrentUser(r.Context())
	rows, err := d.Q.ConnectionsListForUser(r.Context(), viewer.ID)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	data := pageData{Loc: location(viewer.Timezone)}
	for _, a := range d.SyncAdapters.All() {
		data.Adapters = append(data.Adapters, adapterView{Provider: a.Provider(), Caps: a.Capabilities(), Configured: a.Configured()})
	}
	for _, row := range rows {
		v := accountView{ConnectionsListForUserRow: row, ProviderName: row.Provider}
		if a, ok := d.SyncAdapters.Get(row.Provider); ok {
			v.ProviderName = a.Capabilities().DisplayName
			v.Summary = librarySummary(a.Capabilities(), row.LastCounts)
		}
		data.Accounts = append(data.Accounts, v)
	}
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, "Connections", ""), data))
}

func syncNow(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	viewer, _ := auth.CurrentUser(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	acct, err := d.Q.ConnectionsGetForUser(r.Context(), db.ConnectionsGetForUserParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	a, ok := d.SyncAdapters.Get(acct.Provider)
	switch {
	case !ok || !a.Configured():
		web.Flash(r.Context(), d, "This connection can't sync until an administrator configures it.")
	case acct.Status == db.ConnectionStatusDisabled:
		web.Flash(r.Context(), d, "This connection is disabled.")
	default:
		if err := runner.Enqueue(r.Context(), d, nil, acct.ID); err != nil {
			web.Error(w, r, d, err)
			return
		}
		web.Flash(r.Context(), d, "Sync started. Refresh in a minute to see the results.")
	}
	web.Redirect(w, r, "/settings/connections")
}

func librarySummary(c sync.Capabilities, raw []byte) string {
	var counts runner.Counts
	if json.Unmarshal(raw, &counts) != nil || counts.Games == 0 {
		return ""
	}
	noun := "item"
	switch {
	case c.OwnedGames:
		noun = "game"
	case c.Audiobooks:
		noun = "book"
	}
	if counts.Games != 1 {
		noun += "s"
	}
	s := fmt.Sprintf("%d %s in your library", counts.Games, noun)
	if c.Progress {
		s += fmt.Sprintf(", %d with progress", counts.WithProgress)
	}
	return s
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}
