package discover

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const localLimit = 12

func Register(mux *http.ServeMux, d *deps.Deps) {
	wrap := func(mw func(http.Handler) http.Handler, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) http.Handler {
		return mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) }))
	}
	mux.Handle("GET /discover", wrap(auth.RequireUser, discoverPage))
	mux.Handle("POST /discover/import", wrap(auth.RequireUser, importItem))
	mux.Handle("POST /items/{id}/refresh", wrap(auth.RequireAdmin, refreshItem))
	mux.Handle("GET /admin/providers", wrap(auth.RequireAdmin, adminProviders))
}

type pageData struct {
	Kind   db.MediaKind
	Query  string
	Local  []db.CatalogSearchRow
	Groups []providerResults
}

func parseKind(s string) db.MediaKind {
	k := db.MediaKind(s)
	if !k.Valid() {
		return db.MediaKindBook
	}
	return k
}

func discoverPage(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	pd := pageData{Kind: parseKind(r.URL.Query().Get("kind")), Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	if rs := []rune(pd.Query); len(rs) > 200 {
		pd.Query = string(rs[:200])
	}
	if pd.Query != "" {
		var err error
		pd.Local, err = d.Q.CatalogSearch(r.Context(), db.CatalogSearchParams{Query: pd.Query, Kind: &pd.Kind, Lim: localLimit})
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		pd.Groups, err = searchProviders(r.Context(), d, pd.Kind, pd.Query)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
	}
	if web.IsPartial(r) {
		web.Render(w, r, http.StatusOK, results(pd))
		return
	}
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, "Discover", "discover"), pd))
}

func discoverURL(kind db.MediaKind, q string) string {
	v := url.Values{"kind": {string(kind)}}
	if q != "" {
		v.Set("q", q)
	}
	return "/discover?" + v.Encode()
}

func importItem(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	kind := db.MediaKind(f.Get("kind"))
	back := discoverURL(parseKind(f.Get("kind")), f.Get("q"))
	f.Required("provider", "kind", "ns", "id")
	p, ok := d.Providers.Get(f.Get("provider"))
	if !f.Valid() || !kind.Valid() || !ok || !p.Configured() || !slices.Contains(p.Kinds(), kind) {
		web.Flash(r.Context(), d, "That result can't be imported.")
		web.Redirect(w, r, back)
		return
	}
	existing, err := d.Q.FindMediaItemByExternalID(r.Context(), db.FindMediaItemByExternalIDParams{Provider: f.Get("ns"), Value: f.Get("id")})
	if err == nil {
		web.Flash(r.Context(), d, "Already in the catalog.")
		web.Redirect(w, r, "/items/"+existing.PublicID.String())
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		web.Error(w, r, d, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), FetchTimeout)
	defer cancel()
	it, err := p.Fetch(ctx, kind, f.Get("id"))
	recordResult(p.Name(), err, d.Now())
	if err != nil {
		d.Log.Warn("discover: provider fetch failed", "provider", p.Name(), "id", f.Get("id"), "err", err)
		web.Flash(r.Context(), d, "Couldn't import from "+p.Name()+". Try again later.")
		web.Redirect(w, r, back)
		return
	}
	item, err := Import(r.Context(), d, p.Name(), it, &viewer.ID)
	if errors.Is(err, ErrInvalidImport) {
		web.Flash(r.Context(), d, "That result can't be imported.")
		web.Redirect(w, r, back)
		return
	}
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Imported "+item.Title+".")
	web.Redirect(w, r, "/items/"+item.PublicID.String())
}

func refreshItem(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	item, err := d.Q.GetMediaItemByPublicID(r.Context(), id)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	_, err = Refresh(r.Context(), d, item.ID)
	switch {
	case err == nil:
		web.Flash(r.Context(), d, "Metadata refreshed.")
	case errors.Is(err, ErrNoSource):
		web.Flash(r.Context(), d, "This item wasn't imported from a provider.")
	default:
		d.Log.Warn("discover: refresh failed", "media_item_id", item.ID, "err", err)
		web.Flash(r.Context(), d, "Refresh failed. See the provider status page.")
	}
	web.Redirect(w, r, "/items/"+item.PublicID.String())
}

type providerRow struct {
	Name       string
	Kinds      []db.MediaKind
	Configured bool
	Status     providerStatus
}

func adminProviders(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	var rows []providerRow
	for _, p := range d.Providers.All() {
		rows = append(rows, providerRow{Name: p.Name(), Kinds: p.Kinds(), Configured: p.Configured(), Status: statusFor(p.Name())})
	}
	web.Render(w, r, http.StatusOK, adminPage(web.PageFor(r, d, "Providers", ""), rows))
}
