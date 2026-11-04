package journal

import (
	"net/http"
	"net/url"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/ui"
)

const pageSize = 50

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.Handle("GET /journal", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index(w, r, d)
	})))
}

var verbLabels = map[db.ActivityVerb]string{
	db.ActivityVerbAdded:      "Added",
	db.ActivityVerbStarted:    "Started",
	db.ActivityVerbPaused:     "Paused",
	db.ActivityVerbResumed:    "Resumed",
	db.ActivityVerbProgressed: "Progress",
	db.ActivityVerbFinished:   "Finished",
	db.ActivityVerbAbandoned:  "Abandoned",
	db.ActivityVerbRated:      "Rated",
	db.ActivityVerbReviewed:   "Reviewed",
	db.ActivityVerbListed:     "Listed",
}

func index(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	viewer, _ := auth.CurrentUser(r.Context())
	qs := r.URL.Query()

	var f Filter
	data := pageData{
		Kinds:   []ui.Option{{Value: "", Label: "All kinds"}},
		Verbs:   []ui.Option{{Value: "", Label: "All activity"}},
		Now:     d.Now(),
		Display: viewer.RatingDisplay,
	}
	for _, k := range db.AllMediaKindValues() {
		data.Kinds = append(data.Kinds, ui.Option{Value: string(k), Label: catalog.KindLabel(k)})
	}
	for _, v := range db.AllActivityVerbValues() {
		data.Verbs = append(data.Verbs, ui.Option{Value: string(v), Label: verbLabels[v]})
	}
	if k := db.MediaKind(qs.Get("kind")); k.Valid() {
		f.Kind, data.Kind = &k, string(k)
	}
	if v := db.ActivityVerb(qs.Get("verb")); v.Valid() {
		f.Verb, data.Verb = &v, string(v)
	}
	data.Filtered = f.Kind != nil || f.Verb != nil

	before, err1 := ParseCursor(qs.Get("before"))
	after, err2 := ParseCursor(qs.Get("after"))
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	if before != nil {
		after = nil
	}

	p, err := List(r.Context(), d.Q, viewer.ID, f, before, after, pageSize)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	data.Days = GroupByDay(p.Events, Location(viewer))
	link := func(key string, c *Cursor) string {
		if c == nil {
			return ""
		}
		v := url.Values{}
		if data.Kind != "" {
			v.Set("kind", data.Kind)
		}
		if data.Verb != "" {
			v.Set("verb", data.Verb)
		}
		v.Set(key, c.String())
		return "/journal?" + v.Encode()
	}
	data.PrevHref = link("after", p.Newer)
	data.NextHref = link("before", p.Older)
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, "Journal", "journal"), data))
}
