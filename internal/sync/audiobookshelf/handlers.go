package audiobookshelf

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const linkPath = "/connections/audiobookshelf/link"

// Register serves /connections/audiobookshelf/link. The API key is verified
// against the instance's server and stored encrypted; it's never logged or rendered.
func Register(mux *http.ServeMux, d *deps.Deps) {
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) {
		mux.Handle(pattern, auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) })))
	}
	handle("GET "+linkPath, linkForm)
	handle("POST "+linkPath, link)
}

func adapter(d *deps.Deps) (*Adapter, bool) {
	a, ok := d.SyncAdapters.Get(Provider)
	if !ok || !a.Configured() {
		return nil, false
	}
	ab, ok := a.(*Adapter)
	return ab, ok
}

func unavailable(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	web.Flash(r.Context(), d, "Audiobookshelf linking isn't available until an administrator configures it.")
	web.Redirect(w, r, "/settings/connections")
}

func linkForm(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	if _, ok := adapter(d); !ok {
		unavailable(w, r, d)
		return
	}
	web.Render(w, r, http.StatusOK, linkPage(web.PageFor(r, d, "Link Audiobookshelf", ""), form.New(nil)))
}

func link(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	ctx := r.Context()
	viewer, _ := auth.CurrentUser(ctx)
	a, ok := adapter(d)
	if !ok {
		unavailable(w, r, d)
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	f.Required("api_key")
	f.MaxLen("api_key", 4096)
	page := web.PageFor(r, d, "Link Audiobookshelf", "")
	fail := func(status int, msg string) {
		f.Values.Del("api_key")
		if msg != "" {
			f.Errors["_form"] = msg
		}
		web.Render(w, r, status, linkPage(page, f))
	}
	if !f.Valid() {
		fail(http.StatusUnprocessableEntity, "")
		return
	}
	key := f.Get("api_key")

	me, err := a.Me(ctx, key)
	switch {
	case errors.Is(err, ErrUnauthorized):
		fail(http.StatusUnprocessableEntity, "Audiobookshelf didn't accept that API key. Check that you copied all of it and that it hasn't expired.")
		return
	case err != nil:
		d.Log.Error("audiobookshelf link failed", "user_id", viewer.ID, "err", err)
		fail(http.StatusBadGateway, "Couldn't reach Audiobookshelf right now. Try again in a few minutes.")
		return
	}
	creds, err := d.Secrets.Encrypt([]byte(key))
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	var row db.AudiobookshelfLinkAccountRow
	err = d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		var err error
		if row, err = q.AudiobookshelfLinkAccount(ctx, db.AudiobookshelfLinkAccountParams{
			UserID: viewer.ID, AbsUserID: me.ID, DisplayName: me.Username, Credentials: creds,
		}); err != nil {
			return err
		}
		return runner.Enqueue(ctx, d, tx, row.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(http.StatusConflict, "That Audiobookshelf account is linked to another OpenShelf user. Each Audiobookshelf account can belong to only one user.")
		return
	}
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	name := me.Username
	if name == "" {
		name = me.ID
	}
	msg := "Linked Audiobookshelf account " + name + ". Your books are importing now; refresh in a minute to see them."
	if !row.Inserted {
		msg = "Updated the API key for Audiobookshelf account " + name + " and started a sync."
	}
	web.Flash(ctx, d, msg)
	web.Redirect(w, r, "/settings/connections")
}
