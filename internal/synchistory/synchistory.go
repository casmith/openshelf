// Package synchistory serves sync history, exclusions, and unlinking (S22).
package synchistory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const (
	runsPerPage    = 20
	maxRunEvents   = 1000
	maxKindEvents  = 500
	maxKeyLen      = 200
	filterReview   = "needs_review"
	connectionsURL = "/settings/connections"
)

func Register(mux *http.ServeMux, d *deps.Deps) {
	h := func(fn func(http.ResponseWriter, *http.Request, *deps.Deps, db.User, db.ConnectedAccount)) http.Handler {
		return auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			viewer, _ := auth.CurrentUser(r.Context())
			id, err := uuid.Parse(r.PathValue("id"))
			if err != nil {
				web.NotFound(w, r, d)
				return
			}
			acct, err := d.Q.SynchistoryGetAccount(r.Context(), db.SynchistoryGetAccountParams{PublicID: id, UserID: viewer.ID})
			if err != nil {
				web.Error(w, r, d, err)
				return
			}
			fn(w, r, d, viewer, acct)
		}))
	}
	mux.Handle("GET /settings/connections/{id}/history", h(history))
	mux.Handle("GET /connections/{id}/unlink", h(unlinkConfirm))
	mux.Handle("POST /connections/{id}/unlink", h(unlink))
	mux.Handle("POST /connections/{id}/exclusions", h(exclude))
	mux.Handle("DELETE /connections/{id}/exclusions/{key}", h(include))
	mux.Handle("POST /connections/{id}/exclusions/{key}/delete", h(include))
}

func historyURL(acct db.ConnectedAccount) string {
	return connectionsURL + "/" + acct.PublicID.String() + "/history"
}

func accountPath(acct db.ConnectedAccount, suffix string) string {
	return "/connections/" + acct.PublicID.String() + suffix
}

func providerName(d *deps.Deps, provider string) string {
	if a, ok := d.SyncAdapters.Get(provider); ok {
		if n := a.Capabilities().DisplayName; n != "" {
			return n
		}
	}
	return provider
}

func accountName(a db.ConnectedAccount) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.ExternalAccountID
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

func encodeCursor(t time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10) + "~" + strconv.FormatInt(id, 10)))
}

func decodeCursor(s string) (time.Time, int64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, false
	}
	ts, ids, ok := strings.Cut(string(b), "~")
	if !ok {
		return time.Time{}, 0, false
	}
	us, err1 := strconv.ParseInt(ts, 10, 64)
	id, err2 := strconv.ParseInt(ids, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, false
	}
	return time.UnixMicro(us), id, true
}

type runView struct {
	db.SynchistoryListRunsRow
	Counts runner.Counts
	Events []eventView
	Open   bool
}

type historyView struct {
	Account   db.ConnectedAccount
	Provider  string
	Loc       *time.Location
	Runs      []runView
	Older     string
	Paged     bool
	Filter    string
	Review    []eventView
	Excluded  []db.SynchistoryListExclusionsRow
	EventsCap int
}

func history(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User, acct db.ConnectedAccount) {
	ctx := r.Context()
	q := r.URL.Query()
	v := historyView{Account: acct, Provider: providerName(d, acct.Provider), Loc: location(viewer.Timezone), EventsCap: maxRunEvents}

	if q.Get("filter") == filterReview {
		v.Filter = filterReview
		rows, err := d.Q.SynchistoryEventsOfKind(ctx, db.SynchistoryEventsOfKindParams{
			AccountID: acct.ID, UserID: viewer.ID, Kind: sync.EventNeedsReview, Lim: maxKindEvents,
		})
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		for _, e := range rows {
			v.Review = append(v.Review, eventView(e))
		}
	} else {
		p := db.SynchistoryListRunsParams{AccountID: acct.ID, Lim: runsPerPage + 1}
		var openRun int64
		var events []eventView
		if runID := q.Get("run"); runID != "" {
			id, err := uuid.Parse(runID)
			if err != nil {
				web.NotFound(w, r, d)
				return
			}
			run, err := d.Q.SynchistoryGetRun(ctx, db.SynchistoryGetRunParams{PublicID: id, AccountID: acct.ID})
			if err != nil {
				web.Error(w, r, d, err)
				return
			}
			if events, err = runEvents(ctx, d, viewer, acct, run.ID); err != nil {
				web.Error(w, r, d, err)
				return
			}
			if web.IsPartial(r) {
				web.Render(w, r, http.StatusOK, eventList(v, events))
				return
			}
			// Start the page at the requested run so it's shown expanded.
			openRun = run.ID
			p.HasCursor, p.CursorStartedAt, p.CursorID = true, run.StartedAt, run.ID+1
			v.Paged = true
		} else if t, id, ok := decodeCursor(q.Get("before")); ok {
			p.HasCursor, p.CursorStartedAt, p.CursorID = true, t, id
			v.Paged = true
		}
		rows, err := d.Q.SynchistoryListRuns(ctx, p)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		if len(rows) > runsPerPage {
			rows = rows[:runsPerPage]
			last := rows[len(rows)-1]
			v.Older = historyURL(acct) + "?before=" + encodeCursor(last.StartedAt, last.ID)
		}
		for _, row := range rows {
			rv := runView{SynchistoryListRunsRow: row, Counts: decodeCounts(row.Counts)}
			if row.ID == openRun {
				rv.Events, rv.Open = events, true
			}
			v.Runs = append(v.Runs, rv)
		}
	}

	excluded, err := d.Q.SynchistoryListExclusions(ctx, acct.ID)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	v.Excluded = excluded
	web.Render(w, r, http.StatusOK, historyPage(web.PageFor(r, d, "Sync history", ""), v))
}

type eventView db.SynchistoryEventsOfKindRow

func runEvents(ctx context.Context, d *deps.Deps, viewer db.User, acct db.ConnectedAccount, runID int64) ([]eventView, error) {
	rows, err := d.Q.SynchistoryRunEvents(ctx, db.SynchistoryRunEventsParams{RunID: runID, AccountID: acct.ID, UserID: viewer.ID, Lim: maxRunEvents})
	if err != nil {
		return nil, err
	}
	out := make([]eventView, len(rows))
	for i, e := range rows {
		out[i] = eventView(e)
	}
	return out, nil
}

func decodeCounts(b []byte) runner.Counts {
	var c runner.Counts
	_ = json.Unmarshal(b, &c)
	return c
}

// safeNext keeps redirects on this site.
func safeNext(next, fallback string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.HasPrefix(next, "/\\") {
		return next
	}
	return fallback
}

func exclude(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User, acct db.ConnectedAccount) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	key := f.Get("key")
	if key == "" || len(key) > maxKeyLen {
		http.Error(w, "key is required", http.StatusUnprocessableEntity)
		return
	}
	var itemID *int64
	if s := f.Get("item"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			if item, err := d.Q.GetMediaItemByPublicID(r.Context(), id); err == nil {
				itemID = &item.ID
			}
		}
	}
	if err := d.Q.SynchistoryExclude(r.Context(), db.SynchistoryExcludeParams{AccountID: acct.ID, Key: key, MediaItemID: itemID}); err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Excluded from "+providerName(d, acct.Provider)+" sync. What's already imported stays in place.")
	web.Redirect(w, r, safeNext(f.Get("next"), historyURL(acct)))
}

func include(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User, acct db.ConnectedAccount) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n, err := d.Q.SynchistoryInclude(r.Context(), db.SynchistoryIncludeParams{AccountID: acct.ID, Key: r.PathValue("key")})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	if n == 0 {
		web.NotFound(w, r, d)
		return
	}
	web.Flash(r.Context(), d, "Included again. The next sync will import it.")
	next := f.Get("next")
	if next == "" {
		next = r.URL.Query().Get("next")
	}
	web.Redirect(w, r, safeNext(next, historyURL(acct)))
}

type unlinkView struct {
	Account  db.ConnectedAccount
	Provider string
	Remove   []db.SynchistoryUnlinkEntriesRow
	Keep     []db.SynchistoryUnlinkEntriesRow
}

func splitUnlink(rows []db.SynchistoryUnlinkEntriesRow) (remove, keep []db.SynchistoryUnlinkEntriesRow) {
	for _, e := range rows {
		if e.HasUserData || e.OtherAccount {
			keep = append(keep, e)
		} else {
			remove = append(remove, e)
		}
	}
	return remove, keep
}

func unlinkConfirm(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User, acct db.ConnectedAccount) {
	rows, err := d.Q.SynchistoryUnlinkEntries(r.Context(), db.SynchistoryUnlinkEntriesParams{AccountID: acct.ID, UserID: viewer.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	v := unlinkView{Account: acct, Provider: providerName(d, acct.Provider)}
	v.Remove, v.Keep = splitUnlink(rows)
	web.Render(w, r, http.StatusOK, unlinkPage(web.PageFor(r, d, "Unlink account", ""), v))
}

func unlink(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User, acct db.ConnectedAccount) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	mode := f.Get("mode")
	if mode != "keep" && mode != "remove" {
		http.Error(w, "mode must be keep or remove", http.StatusUnprocessableEntity)
		return
	}
	removed, kept, err := Unlink(r.Context(), d, viewer, acct, mode == "remove")
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	msg := "Unlinked " + providerName(d, acct.Provider) + " account " + accountName(acct) + "."
	if mode == "remove" {
		msg += fmt.Sprintf(" Removed %d imported %s", removed, plural(removed, "entry", "entries"))
		if kept > 0 {
			msg += fmt.Sprintf("; kept %d you had changed", kept)
		}
		msg += "."
	} else {
		msg += " Imported entries were kept."
	}
	web.Flash(r.Context(), d, msg)
	web.Redirect(w, r, connectionsURL)
}

// Unlink deletes the account (cascading its runs, events, exclusions, and source
// facts). With remove, it first deletes the entries the account created that
// carry no user data. Catalog items are never deleted.
func Unlink(ctx context.Context, d *deps.Deps, viewer db.User, acct db.ConnectedAccount, remove bool) (removed, kept int, err error) {
	err = d.WithTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		removed, kept = 0, 0
		if remove {
			rows, err := q.SynchistoryUnlinkEntries(ctx, db.SynchistoryUnlinkEntriesParams{AccountID: acct.ID, UserID: viewer.ID})
			if err != nil {
				return err
			}
			del, keep := splitUnlink(rows)
			kept = len(keep)
			for _, e := range del {
				if _, err := q.LibraryDeleteEntry(ctx, db.LibraryDeleteEntryParams{EntryID: e.PublicID, UserID: viewer.ID}); err != nil {
					return err
				}
				removed++
			}
		}
		n, err := q.SynchistoryDeleteAccount(ctx, db.SynchistoryDeleteAccountParams{ID: acct.ID, UserID: viewer.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
	return removed, kept, err
}

// ExcludeRemovedItem runs in the transaction that deleted the viewer's entry for an
// item: once no entry remains, each linked account's key for it is excluded so the
// next sync doesn't re-import it.
func ExcludeRemovedItem(ctx context.Context, q *db.Queries, viewer db.User, itemPublicID uuid.UUID) error {
	return q.SynchistoryExcludeRemovedItem(ctx, db.SynchistoryExcludeRemovedItemParams{UserID: viewer.ID, ItemPublicID: itemPublicID})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
