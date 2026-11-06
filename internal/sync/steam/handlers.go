package steam

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/web"
)

const (
	stateKey      = "steam_openid_state"
	callbackPath  = "/connections/steam/callback"
	nonceRetained = 24 * time.Hour
)

// Register serves /connections/steam/*. Steam sign-in only binds a SteamID to the
// user who is already logged in; it never logs anyone into OpenShelf.
func Register(mux *http.ServeMux, d *deps.Deps) {
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) {
		mux.Handle(pattern, auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) })))
	}
	handle("GET /connections/steam/link", link)
	handle("GET "+callbackPath, callback)
}

func adapter(d *deps.Deps) (*Adapter, bool) {
	a, ok := d.SyncAdapters.Get(Provider)
	if !ok || !a.Configured() {
		return nil, false
	}
	st, ok := a.(*Adapter)
	return st, ok
}

func returnTo(d *deps.Deps, state string) string {
	return d.Config.BaseURL + callbackPath + "?" + url.Values{"state": {state}}.Encode()
}

func link(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	a, ok := adapter(d)
	if !ok {
		web.Flash(r.Context(), d, "Steam linking isn't available until an administrator configures it.")
		web.Redirect(w, r, "/settings/connections")
		return
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state := base64.RawURLEncoding.EncodeToString(b)
	d.Sessions.Put(r.Context(), stateKey, state)
	target := a.LoginURL(returnTo(d, state), d.Config.BaseURL)
	// A boosted link would follow the redirect over XHR, which can't leave the origin.
	if web.IsHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func callback(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	ctx := r.Context()
	viewer, _ := auth.CurrentUser(ctx)
	fail := func(status int, title, body string) {
		web.Render(w, r, status, failurePage(web.PageFor(r, d, title, ""), body))
	}
	a, ok := adapter(d)
	if !ok {
		fail(http.StatusNotFound, "Steam isn't available", "An administrator needs to configure Steam before accounts can be linked.")
		return
	}
	params := r.URL.Query()
	want := d.Sessions.PopString(ctx, stateKey)
	if params.Get("openid.mode") == "cancel" {
		web.Flash(ctx, d, "Steam linking was cancelled.")
		web.Redirect(w, r, "/settings/connections")
		return
	}
	if want == "" || subtle.ConstantTimeCompare([]byte(params.Get("state")), []byte(want)) != 1 {
		fail(http.StatusBadRequest, "Couldn't link Steam", "This Steam sign-in didn't start here or has expired. Start linking again from Connections.")
		return
	}

	now := d.Now()
	steamID, nonce, err := a.VerifyAssertion(ctx, params, returnTo(d, want), now)
	switch {
	case errors.Is(err, ErrOpenIDInvalid):
		d.Log.Warn("steam openid rejected", "user_id", viewer.ID, "err", err)
		fail(http.StatusBadRequest, "Couldn't link Steam", "Steam's sign-in response couldn't be verified. Start linking again from Connections.")
		return
	case err != nil:
		d.Log.Error("steam openid verification failed", "err", err)
		fail(http.StatusBadGateway, "Couldn't reach Steam", "Steam couldn't confirm the sign-in right now. Try again in a few minutes.")
		return
	}
	if err := d.Q.SteamPruneNonces(ctx, now.Add(-nonceRetained)); err != nil {
		web.Error(w, r, d, err)
		return
	}
	if n, err := d.Q.SteamUseNonce(ctx, nonce); err != nil {
		web.Error(w, r, d, err)
		return
	} else if n == 0 {
		fail(http.StatusBadRequest, "Couldn't link Steam", "This Steam sign-in was already used. Start linking again from Connections.")
		return
	}

	p := db.SteamLinkAccountParams{UserID: viewer.ID, SteamID: steamID}
	profile, err := a.PlayerSummary(ctx, steamID)
	if err != nil {
		d.Log.Warn("steam profile lookup failed", "steam_id", steamID, "err", err)
	}
	if profile != nil {
		p.DisplayName = profile.PersonaName
		p.ProfileUrl, p.AvatarUrl = optString(profile.ProfileURL), optString(profile.AvatarMedium)
	}
	var row db.SteamLinkAccountRow
	err = d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		var err error
		if row, err = q.SteamLinkAccount(ctx, p); err != nil {
			return err
		}
		return runner.Enqueue(ctx, d, tx, row.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(http.StatusConflict, "Steam account already linked", "That Steam account is linked to another OpenShelf user. Each Steam account can belong to only one user.")
		return
	}
	if err != nil {
		web.Error(w, r, d, err)
		return
	}

	name := steamID
	if p.DisplayName != "" {
		name = p.DisplayName
	}
	msg := "Linked Steam account " + name + ". Your games are importing now; refresh in a minute to see them."
	if !row.Inserted {
		msg = "Updated Steam account " + name + " and started a sync."
	}
	if profile != nil && profile.CommunityVisibilityState != visibilityPublic {
		msg += " Your Steam profile is private, so nothing can be imported until you set it and your game details to Public."
	}
	web.Flash(ctx, d, msg)
	web.Redirect(w, r, "/settings/connections")
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
