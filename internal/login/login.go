package login

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

type handlers struct {
	d   *deps.Deps
	lim *limiter
}

func Register(mux *http.ServeMux, d *deps.Deps) {
	h := &handlers{d: d, lim: newLimiter(6*time.Second, 10, d.Config.TrustedProxies)}
	mux.HandleFunc("GET /login", h.show)
	mux.HandleFunc("POST /login", h.submit)
	mux.HandleFunc("POST /logout", h.logout)
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.CurrentUser(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if n, err := h.d.Q.CountUsers(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	f := form.New(nil)
	f.Values.Set("next", auth.SafeNext(r.URL.Query().Get("next")))
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, h.d, "Sign in", ""), f))
}

func (h *handlers) submit(w http.ResponseWriter, r *http.Request) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !h.lim.allow(r) {
		f.Add("_form", "Too many attempts. Try again in a minute.")
		web.Render(w, r, http.StatusTooManyRequests, page(web.PageFor(r, h.d, "Sign in", ""), f))
		return
	}
	f.Required("username", "password")
	if !f.Valid() {
		web.Render(w, r, http.StatusUnprocessableEntity, page(web.PageFor(r, h.d, "Sign in", ""), f))
		return
	}

	u, err := h.d.Q.GetUserByUsername(r.Context(), f.Get("username"))
	hash := u.PasswordHash
	if errors.Is(err, pgx.ErrNoRows) {
		hash = dummyHash
	} else if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	ok, err := auth.VerifyPassword(f.Values.Get("password"), hash)
	if err != nil && !errors.Is(err, auth.ErrBadHash) {
		web.Error(w, r, h.d, err)
		return
	}
	if !ok || u.ID == 0 || u.DisabledAt != nil {
		f.Add("_form", "Incorrect username or password.")
		f.Values.Del("password")
		web.Render(w, r, http.StatusUnprocessableEntity, page(web.PageFor(r, h.d, "Sign in", ""), f))
		return
	}

	if err := auth.LogIn(r.Context(), h.d, u.ID); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	http.Redirect(w, r, auth.SafeNext(f.Get("next")), http.StatusSeeOther)
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	if err := auth.LogOut(r.Context(), h.d); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// dummyHash is verified when the username doesn't exist so timing doesn't reveal registered names.
var dummyHash, _ = auth.HashPassword("openshelf-dummy-password")
