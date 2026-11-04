package accounts

import (
	"errors"
	"net/http"
	"strings"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const (
	maxDisplayName = 100
	maxEmail       = 254
	maxPassword    = 1024
)

type handlers struct{ d *deps.Deps }

func Register(mux *http.ServeMux, d *deps.Deps) {
	h := &handlers{d: d}
	user := func(f http.HandlerFunc) http.Handler { return auth.RequireUser(f) }
	admin := func(f http.HandlerFunc) http.Handler { return auth.RequireAdmin(f) }

	mux.HandleFunc("GET /setup", h.setupForm)
	mux.HandleFunc("POST /setup", h.setup)

	mux.Handle("GET /settings", user(h.settings))
	mux.Handle("POST /settings/profile", user(h.updateProfile))
	mux.Handle("POST /settings/password", user(h.changePassword))
	mux.Handle("POST /settings/preferences", user(h.updatePreferences))

	mux.Handle("GET /admin/users", admin(h.adminUsers))
	mux.Handle("GET /admin/users/new", admin(h.adminNewUser))
	mux.Handle("POST /admin/users", admin(h.adminCreateUser))
	mux.Handle("POST /admin/users/{username}/role", admin(h.adminSetRole))
	mux.Handle("POST /admin/users/{username}/disable", admin(h.adminDisable(true)))
	mux.Handle("POST /admin/users/{username}/enable", admin(h.adminDisable(false)))
	mux.Handle("POST /admin/users/{username}/reset-password", admin(h.adminResetPassword))
}

func viewer(r *http.Request) db.User {
	u, _ := auth.CurrentUser(r.Context())
	return u
}

func parse(w http.ResponseWriter, r *http.Request) (*form.Form, bool) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	return f, true
}

func validateNewPassword(f *form.Form, field, confirm string) {
	pw := f.Values.Get(field)
	switch {
	case pw == "":
		f.Add(field, "This field is required.")
	case len(pw) < auth.MinPassword:
		f.Add(field, "Must be at least 8 characters.")
	case len(pw) > maxPassword:
		f.Add(field, "Too long.")
	case pw != f.Values.Get(confirm):
		f.Add(confirm, "Passwords don't match.")
	}
}

func validateProfile(f *form.Form) {
	f.Required("display_name")
	f.MaxLen("display_name", maxDisplayName)
	f.MaxLen("email", maxEmail)
	if e := f.Get("email"); e != "" && !ValidEmail(e) {
		f.Add("email", "Enter a valid email address.")
	}
}

func addUniqueErr(f *form.Form, err error) bool {
	switch {
	case errors.Is(err, ErrUsernameTaken):
		f.Add("username", "That username is taken.")
	case errors.Is(err, ErrEmailTaken):
		f.Add("email", "That email is already in use.")
	default:
		return false
	}
	return true
}

// Setup

func (h *handlers) needsSetup(w http.ResponseWriter, r *http.Request) bool {
	n, err := h.d.Q.CountUsers(r.Context())
	if err != nil {
		web.Error(w, r, h.d, err)
		return false
	}
	if n > 0 {
		web.NotFound(w, r, h.d)
		return false
	}
	return true
}

func (h *handlers) setupForm(w http.ResponseWriter, r *http.Request) {
	if !h.needsSetup(w, r) {
		return
	}
	web.Render(w, r, http.StatusOK, setupPage(web.PageFor(r, h.d, "Set up OpenShelf", ""), form.New(nil)))
}

func (h *handlers) setup(w http.ResponseWriter, r *http.Request) {
	if !h.needsSetup(w, r) {
		return
	}
	f, ok := parse(w, r)
	if !ok {
		return
	}
	username := strings.ToLower(f.Get("username"))
	f.Values.Set("username", username)
	f.Required("username")
	if username != "" && !ValidUsername(username) {
		f.Add("username", "Use 2–30 lowercase letters, digits, or underscores.")
	}
	if f.Get("display_name") == "" {
		f.Values.Set("display_name", username)
	}
	validateProfile(f)
	validateNewPassword(f, "password", "password_confirm")
	render := func() {
		f.Values.Del("password")
		f.Values.Del("password_confirm")
		web.Render(w, r, http.StatusUnprocessableEntity, setupPage(web.PageFor(r, h.d, "Set up OpenShelf", ""), f))
	}
	if !f.Valid() {
		render()
		return
	}
	u, err := Setup(r.Context(), h.d, NewUser{
		Username: username, DisplayName: f.Get("display_name"), Email: f.Optional("email"), Password: f.Values.Get("password"),
	})
	switch {
	case errors.Is(err, ErrSetupDone):
		web.NotFound(w, r, h.d)
		return
	case addUniqueErr(f, err):
		render()
		return
	case err != nil:
		web.Error(w, r, h.d, err)
		return
	}
	if err := auth.LogIn(r.Context(), h.d, u.ID); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	r = r.WithContext(auth.WithUser(r.Context(), u))
	web.Render(w, r, http.StatusOK, setupDonePage(web.PageFor(r, h.d, "Setup complete", ""), h.providerStatus()))
}

type providerStatus struct {
	Label      string
	Kinds      string
	Env        string
	Configured bool
}

var providerInfo = map[string]providerStatus{
	"openlibrary": {Label: "Open Library", Kinds: "Books", Env: "no key required"},
	"tmdb":        {Label: "TMDB", Kinds: "Movies and TV", Env: "OPENSHELF_TMDB_TOKEN"},
	"igdb":        {Label: "IGDB", Kinds: "Games", Env: "OPENSHELF_IGDB_CLIENT_ID, OPENSHELF_IGDB_CLIENT_SECRET"},
}

func (h *handlers) providerStatus() []providerStatus {
	var out []providerStatus
	if h.d.Providers != nil {
		for _, p := range h.d.Providers.All() {
			s, ok := providerInfo[p.Name()]
			if !ok {
				s = providerStatus{Label: p.Name()}
			}
			s.Configured = p.Configured()
			out = append(out, s)
		}
	}
	return append(out, providerStatus{
		Label: "Steam", Kinds: "Game library import", Env: "OPENSHELF_STEAM_API_KEY", Configured: h.d.Config.SteamAPIKey != "",
	})
}

// Settings

type settingsData struct {
	User                    db.User
	Profile, Password, Pref *form.Form
}

func newSettings(u db.User) settingsData {
	p := form.New(nil)
	p.Values.Set("display_name", u.DisplayName)
	if u.Email != nil {
		p.Values.Set("email", *u.Email)
	}
	pref := form.New(nil)
	pref.Values.Set("default_visibility", string(u.DefaultVisibility))
	pref.Values.Set("rating_display", string(u.RatingDisplay))
	pref.Values.Set("timezone", u.Timezone)
	return settingsData{User: u, Profile: p, Password: form.New(nil), Pref: pref}
}

func (h *handlers) renderSettings(w http.ResponseWriter, r *http.Request, status int, s settingsData) {
	web.Render(w, r, status, settingsPage(web.PageFor(r, h.d, "Settings", ""), s))
}

func (h *handlers) settings(w http.ResponseWriter, r *http.Request) {
	h.renderSettings(w, r, http.StatusOK, newSettings(viewer(r)))
}

func (h *handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	f, ok := parse(w, r)
	if !ok {
		return
	}
	v := viewer(r)
	validateProfile(f)
	s := newSettings(v)
	s.Profile = f
	if !f.Valid() {
		h.renderSettings(w, r, http.StatusUnprocessableEntity, s)
		return
	}
	_, err := UpdateProfile(r.Context(), h.d.Q, v.ID, f.Get("display_name"), f.Optional("email"))
	if addUniqueErr(f, err) {
		h.renderSettings(w, r, http.StatusUnprocessableEntity, s)
		return
	} else if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Profile saved.")
	web.Redirect(w, r, "/settings")
}

func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	f, ok := parse(w, r)
	if !ok {
		return
	}
	v := viewer(r)
	s := newSettings(v)
	s.Password = form.New(nil)
	s.Password.Errors = f.Errors
	cur := f.Values.Get("current_password")
	if cur == "" {
		f.Add("current_password", "This field is required.")
	} else if ok, err := auth.VerifyPassword(cur, v.PasswordHash); err != nil && !errors.Is(err, auth.ErrBadHash) {
		web.Error(w, r, h.d, err)
		return
	} else if !ok {
		f.Add("current_password", "Current password is incorrect.")
	}
	validateNewPassword(f, "new_password", "new_password_confirm")
	if !f.Valid() {
		h.renderSettings(w, r, http.StatusUnprocessableEntity, s)
		return
	}
	if err := SetPassword(r.Context(), h.d.Q, v.ID, f.Values.Get("new_password")); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if err := auth.LogIn(r.Context(), h.d, v.ID); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Password changed.")
	web.Redirect(w, r, "/settings")
}

func (h *handlers) updatePreferences(w http.ResponseWriter, r *http.Request) {
	f, ok := parse(w, r)
	if !ok {
		return
	}
	v := viewer(r)
	f.Required("default_visibility", "rating_display", "timezone")
	vis := db.Visibility(f.Get("default_visibility"))
	if !vis.Valid() {
		f.Add("default_visibility", "Invalid choice.")
	}
	rd := db.RatingDisplay(f.Get("rating_display"))
	if !rd.Valid() {
		f.Add("rating_display", "Invalid choice.")
	}
	if !ValidTimezone(f.Get("timezone")) {
		f.Add("timezone", "Choose a valid time zone.")
	}
	if !f.Valid() {
		s := newSettings(v)
		s.Pref = f
		h.renderSettings(w, r, http.StatusUnprocessableEntity, s)
		return
	}
	if _, err := h.d.Q.AccountsUpdatePreferences(r.Context(), db.AccountsUpdatePreferencesParams{
		ID: v.ID, DefaultVisibility: vis, RatingDisplay: rd, Timezone: f.Get("timezone"),
	}); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Preferences saved.")
	web.Redirect(w, r, "/settings")
}

// Admin

func (h *handlers) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.d.Q.AccountsListUsers(r.Context())
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Render(w, r, http.StatusOK, adminUsersPage(web.PageFor(r, h.d, "Users", ""), viewer(r), users))
}

func (h *handlers) adminNewUser(w http.ResponseWriter, r *http.Request) {
	web.Render(w, r, http.StatusOK, adminNewUserPage(web.PageFor(r, h.d, "New user", ""), form.New(nil)))
}

func (h *handlers) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	f, ok := parse(w, r)
	if !ok {
		return
	}
	username := strings.ToLower(f.Get("username"))
	f.Values.Set("username", username)
	f.Required("username")
	if username != "" && !ValidUsername(username) {
		f.Add("username", "Use 2–30 lowercase letters, digits, or underscores.")
	}
	if f.Get("display_name") == "" {
		f.Values.Set("display_name", username)
	}
	validateProfile(f)
	role := db.UserRoleMember
	if f.Bool("admin") {
		role = db.UserRoleAdmin
	}
	render := func() {
		web.Render(w, r, http.StatusUnprocessableEntity, adminNewUserPage(web.PageFor(r, h.d, "New user", ""), f))
	}
	if !f.Valid() {
		render()
		return
	}
	pw := TempPassword()
	u, err := CreateUser(r.Context(), h.d.Q, NewUser{
		Username: username, DisplayName: f.Get("display_name"), Email: f.Optional("email"), Password: pw, Role: role,
	})
	if addUniqueErr(f, err) {
		render()
		return
	} else if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Render(w, r, http.StatusOK, tempPasswordPage(web.PageFor(r, h.d, "User created", ""), u, pw, true))
}

func (h *handlers) adminResult(w http.ResponseWriter, r *http.Request, err error, okMsg, next string) {
	switch {
	case errors.Is(err, ErrLastAdmin):
		web.Flash(r.Context(), h.d, "The last admin can't be demoted or disabled.")
		next = "/admin/users"
	case errors.Is(err, ErrSelf):
		web.Flash(r.Context(), h.d, "You can't disable your own account.")
		next = "/admin/users"
	case err != nil:
		web.Error(w, r, h.d, err)
		return
	default:
		web.Flash(r.Context(), h.d, okMsg)
	}
	web.Redirect(w, r, next)
}

func (h *handlers) adminSetRole(w http.ResponseWriter, r *http.Request) {
	f, ok := parse(w, r)
	if !ok {
		return
	}
	role := db.UserRole(f.Get("role"))
	if !role.Valid() {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	username := r.PathValue("username")
	next := "/admin/users"
	if username == viewer(r).Username && role != db.UserRoleAdmin {
		next = "/settings"
	}
	h.adminResult(w, r, SetRole(r.Context(), h.d, username, role), "Role updated.", next)
}

func (h *handlers) adminDisable(disabled bool) http.HandlerFunc {
	msg := "Account enabled."
	if disabled {
		msg = "Account disabled."
	}
	return func(w http.ResponseWriter, r *http.Request) {
		err := SetDisabled(r.Context(), h.d, viewer(r), r.PathValue("username"), disabled)
		h.adminResult(w, r, err, msg, "/admin/users")
	}
}

func (h *handlers) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	u, pw, err := ResetPassword(r.Context(), h.d, r.PathValue("username"))
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Render(w, r, http.StatusOK, tempPasswordPage(web.PageFor(r, h.d, "Password reset", ""), u, pw, false))
}
