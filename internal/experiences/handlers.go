package experiences

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const maxNotes = 10000

type handlers struct{ d *deps.Deps }

// Register adds this package's routes.
func Register(mux *http.ServeMux, d *deps.Deps) {
	h := handlers{d}
	handle := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, auth.RequireUser(fn)) }
	handle("POST /items/{id}/experiences", h.create)
	for _, a := range rowActions {
		handle("POST /experiences/{id}/"+string(a), h.transition(a))
	}
	handle("POST /experiences/{id}/progress", h.progress)
	handle("GET /experiences/{id}/edit", h.edit)
	handle("POST /experiences/{id}/edit", h.update)
	handle("DELETE /experiences/{id}", h.delete)
}

func viewer(r *http.Request) db.User {
	u, _ := auth.CurrentUser(r.Context())
	return u
}

func pathID(w http.ResponseWriter, r *http.Request, d *deps.Deps) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return id, false
	}
	return id, true
}

// done renders the updated fragment for htmx, or redirects to the item page.
func (h handlers) done(w http.ResponseWriter, r *http.Request, itemID int64, quick bool) {
	item, err := h.d.Q.GetMediaItemByID(r.Context(), itemID)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if !web.IsPartial(r) {
		web.Redirect(w, r, itemURL(item))
		return
	}
	var c templ.Component
	if quick {
		c, err = QuickLog(r.Context(), h.d, viewer(r), item)
	} else {
		c, err = ItemPanel(r.Context(), h.d, viewer(r), item)
	}
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Render(w, r, http.StatusOK, c)
}

// fail answers 422 with msg, as the panel fragment for htmx or a small page otherwise.
func (h handlers) fail(w http.ResponseWriter, r *http.Request, itemID int64, msg string) {
	item, err := h.d.Q.GetMediaItemByID(r.Context(), itemID)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if web.IsPartial(r) {
		c, err := loadPanel(r.Context(), h.d, viewer(r), item, msg)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		web.Render(w, r, http.StatusUnprocessableEntity, c)
		return
	}
	web.Render(w, r, http.StatusUnprocessableEntity, failPage(web.PageFor(r, h.d, item.Title, string(item.Kind)), item, msg))
}

func userError(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidTransition):
		return "That change isn't allowed from the entry's current state.", true
	case errors.Is(err, ErrNoProgress):
		return "This entry doesn't track progress.", true
	case errors.Is(err, ErrBadProgress):
		return "Progress must be a number of zero or more.", true
	}
	return "", false
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, h.d)
	if !ok {
		return
	}
	item, err := h.d.Q.GetMediaItemByPublicID(r.Context(), id)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	v := viewer(r)
	in := CreateInput{Finished: f.Get("mode") == "finished"}
	in.StartedOn, in.FinishedOn = validDates(f, Today(h.d.Now(), v.Timezone))
	if s := f.Get("edition"); s != "" {
		if eid, err := uuid.Parse(s); err == nil {
			in.Edition = &eid
		}
	}
	if !f.Valid() {
		h.fail(w, r, item.ID, firstError(f))
		return
	}
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		_, err := Create(r.Context(), q, h.d.Now(), v, item, in)
		return err
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	h.done(w, r, item.ID, false)
}

func (h handlers) transition(a Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mutate(w, r, func(q *db.Queries, v db.User, id uuid.UUID, _ *form.Form) (db.Experience, error) {
			return Transition(r.Context(), q, h.d.Now(), v, id, a)
		})
	}
}

func (h handlers) progress(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(q *db.Queries, v db.User, id uuid.UUID, f *form.Form) (db.Experience, error) {
		value, err := strconv.ParseFloat(f.Get("value"), 64)
		if err != nil {
			value = -1
		}
		return LogProgress(r.Context(), q, h.d.Now(), v, id, value)
	})
}

type mutation func(q *db.Queries, v db.User, id uuid.UUID, f *form.Form) (db.Experience, error)

func (h handlers) mutate(w http.ResponseWriter, r *http.Request, fn mutation) {
	id, ok := pathID(w, r, h.d)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	v := viewer(r)
	var e db.Experience
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		e, err = fn(q, v, id, f)
		return err
	})
	if msg, ok := userError(err); ok {
		h.fail(w, r, e.MediaItemID, msg)
		return
	}
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	h.done(w, r, e.MediaItemID, f.Get("view") == "quick")
}

func (h handlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, h.d)
	if !ok {
		return
	}
	itemID, err := Delete(r.Context(), h.d.Q, viewer(r), id)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	h.done(w, r, itemID, false)
}

func (h handlers) load(w http.ResponseWriter, r *http.Request) (db.Experience, db.MediaItem, bool) {
	id, ok := pathID(w, r, h.d)
	if !ok {
		return db.Experience{}, db.MediaItem{}, false
	}
	e, err := h.d.Q.ExperiencesGet(r.Context(), db.ExperiencesGetParams{PublicID: id, UserID: viewer(r).ID})
	if err != nil {
		web.Error(w, r, h.d, err)
		return e, db.MediaItem{}, false
	}
	item, err := h.d.Q.GetMediaItemByID(r.Context(), e.MediaItemID)
	if err != nil {
		web.Error(w, r, h.d, err)
		return e, item, false
	}
	return e, item, true
}

func (h handlers) edit(w http.ResponseWriter, r *http.Request) {
	e, item, ok := h.load(w, r)
	if !ok {
		return
	}
	vals := url.Values{"visibility": {string(e.Visibility)}}
	if e.StartedOn != nil {
		vals.Set("started_on", e.StartedOn.Format(time.DateOnly))
	}
	if e.FinishedOn != nil {
		vals.Set("finished_on", e.FinishedOn.Format(time.DateOnly))
	}
	if e.ProgressUnit != nil {
		vals.Set("progress_unit", string(*e.ProgressUnit))
	}
	if e.ProgressTotal != nil {
		vals.Set("progress_total", fmtNum(*e.ProgressTotal))
	}
	if e.Notes != nil {
		vals.Set("notes", *e.Notes)
	}
	h.renderEdit(w, r, http.StatusOK, item, e, form.New(vals))
}

func (h handlers) renderEdit(w http.ResponseWriter, r *http.Request, status int, item db.MediaItem, e db.Experience, f *form.Form) {
	web.Render(w, r, status, editPage(web.PageFor(r, h.d, "Edit entry", string(item.Kind)), item, e, f))
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	e, item, ok := h.load(w, r)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	v := viewer(r)
	in, ok := parseEdit(f, e, Today(h.d.Now(), v.Timezone))
	if !ok {
		h.renderEdit(w, r, http.StatusUnprocessableEntity, item, e, f)
		return
	}
	if _, err := Edit(r.Context(), h.d.Q, h.d.Now(), v, e, in); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Redirect(w, r, itemURL(item))
}

func parseEdit(f *form.Form, e db.Experience, today time.Time) (EditInput, bool) {
	ended := e.State == db.ExperienceStateFinished || e.State == db.ExperienceStateAbandoned
	if ended {
		f.Required("finished_on")
	} else {
		f.Values.Del("finished_on")
	}
	f.Required("visibility")
	f.OneOf("visibility", string(db.VisibilityPrivate), string(db.VisibilityInstance), string(db.VisibilityPublic))
	units := make([]string, 0, len(db.AllProgressUnitValues()))
	for _, u := range db.AllProgressUnitValues() {
		units = append(units, string(u))
	}
	f.OneOf("progress_unit", units...)
	f.MaxLen("notes", maxNotes)

	var in EditInput
	in.StartedOn, in.FinishedOn = validDates(f, today)
	in.Visibility = db.Visibility(f.Get("visibility"))
	in.Notes = f.Optional("notes")
	if u := f.Get("progress_unit"); u != "" {
		pu := db.ProgressUnit(u)
		in.ProgressUnit = &pu
	}
	if s := f.Get("progress_total"); s != "" {
		t, err := strconv.ParseFloat(s, 64)
		if err != nil || t <= 0 || t >= 1e8 {
			f.Add("progress_total", "Must be a positive number.")
		} else {
			in.ProgressTotal = &t
		}
	}
	return in, f.Valid()
}

// validDates parses started_on and finished_on, rejecting future dates and finishes before starts.
func validDates(f *form.Form, today time.Time) (started, finished *time.Time) {
	started, finished = f.Date("started_on"), f.Date("finished_on")
	for key, d := range map[string]*time.Time{"started_on": started, "finished_on": finished} {
		if d != nil && d.After(today) {
			f.Add(key, "Can't be in the future.")
		}
	}
	if started != nil && finished != nil && finished.Before(*started) {
		f.Add("finished_on", "Must be on or after the start date.")
	}
	return started, finished
}

func firstError(f *form.Form) string {
	for k, label := range map[string]string{"started_on": "Start date", "finished_on": "Finish date"} {
		if msg := f.Error(k); msg != "" {
			return label + ": " + msg
		}
	}
	for _, msg := range f.Errors {
		return msg
	}
	return ""
}
