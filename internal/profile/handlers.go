package profile

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{2,30}$`)

// Username extracts the name from a "/@username" path (a trailing slash is allowed).
func Username(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "/@")
	if !ok {
		return "", false
	}
	name = strings.TrimSuffix(name, "/")
	return name, usernameRE.MatchString(name)
}

// Show renders the profile of username. It is dispatched from the catch-all route
// because ServeMux can't match "@{username}" within a segment.
func Show(w http.ResponseWriter, r *http.Request, d *deps.Deps, username string) {
	auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		show(w, r, d, username)
	})).ServeHTTP(w, r)
}

func show(w http.ResponseWriter, r *http.Request, d *deps.Deps, username string) {
	viewer, _ := auth.CurrentUser(r.Context())
	q := r.URL.Query()
	var kind *db.MediaKind
	if k := db.MediaKind(q.Get("kind")); k.Valid() {
		kind = &k
	}
	var before *cursor
	if s := q.Get("before"); s != "" {
		c, err := parseCursor(s)
		if err != nil {
			web.NotFound(w, r, d)
			return
		}
		before = c
	}
	p, err := loadUser(r.Context(), d, username, viewer, kind)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	if web.IsPartial(r) && before != nil {
		if p.Completed, err = loadCompleted(r.Context(), d, p, viewer, before); err != nil {
			web.Error(w, r, d, err)
			return
		}
		web.Render(w, r, http.StatusOK, completedPage(p))
		return
	}
	if p, err = load(r.Context(), d, p, viewer, before); err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, p.Owner.DisplayName, "profile"), p))
}
