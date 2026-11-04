package catalog

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

type handlers struct{ d *deps.Deps }

// Register adds the catalog routes: manual entry, editing, editions, and search.
func Register(mux *http.ServeMux, d *deps.Deps) {
	h := &handlers{d: d}
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, auth.RequireUser(fn)) }
	route("GET /items/new", h.newItem)
	route("POST /items", h.createItem)
	route("GET /items/{id}/edit", h.withItem(h.editItem))
	route("POST /items/{id}/edit", h.withItem(h.updateItem))
	route("GET /items/{id}/editions/new", h.withItem(h.newEdition))
	route("POST /items/{id}/editions", h.withItem(h.createEdition))
	route("GET /items/{id}/editions/platforms", h.platformPicker)
	route("GET /items/{id}/editions/{eid}/edit", h.withItem(h.withEdition(h.editEdition)))
	route("POST /items/{id}/editions/{eid}/edit", h.withItem(h.withEdition(h.updateEdition)))
	route("POST /items/{id}/editions/{eid}/delete", h.withItem(h.withEdition(h.deleteEdition)))
	route("GET /search", h.search)
}

func itemURL(item db.MediaItem) string { return "/items/" + item.PublicID.String() }

func viewer(r *http.Request) db.User {
	u, _ := auth.CurrentUser(r.Context())
	return u
}

type itemHandler func(w http.ResponseWriter, r *http.Request, item db.MediaItem)

// withItem loads {id} and rejects viewers who may not edit it.
func (h *handlers) withItem(next itemHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			web.NotFound(w, r, h.d)
			return
		}
		item, err := h.d.Q.GetMediaItemByPublicID(r.Context(), id)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		if !CanEdit(viewer(r), item) {
			web.Render(w, r, http.StatusForbidden, messagePage(web.PageFor(r, h.d, "Not allowed", string(item.Kind)),
				"You can't edit this item", "Only admins can edit items added by someone else."))
			return
		}
		next(w, r, item)
	}
}

type editionHandler func(w http.ResponseWriter, r *http.Request, item db.MediaItem, e db.Edition)

func (h *handlers) withEdition(next editionHandler) itemHandler {
	return func(w http.ResponseWriter, r *http.Request, item db.MediaItem) {
		eid, err := uuid.Parse(r.PathValue("eid"))
		if err != nil {
			web.NotFound(w, r, h.d)
			return
		}
		e, err := h.d.Q.CatalogGetEdition(r.Context(), db.CatalogGetEditionParams{PublicID: eid, MediaItemID: item.ID})
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		next(w, r, item, e)
	}
}

func kindParam(s string) db.MediaKind {
	if k := db.MediaKind(s); k.Valid() {
		return k
	}
	return db.MediaKindGame
}

func (h *handlers) newItem(w http.ResponseWriter, r *http.Request) {
	kind := kindParam(r.URL.Query().Get("kind"))
	f := form.New(nil)
	f.Values.Set("kind", string(kind))
	if t := r.URL.Query().Get("title"); t != "" {
		f.Values.Set("title", t)
	}
	h.renderItemForm(w, r, http.StatusOK, itemFormData{Kind: kind, Form: f})
}

func (h *handlers) renderItemForm(w http.ResponseWriter, r *http.Request, status int, data itemFormData) {
	title := "Add " + KindLabel(data.Kind)
	if data.Item != nil {
		title = "Edit " + data.Item.Title
	}
	web.Render(w, r, status, itemFormPage(web.PageFor(r, h.d, title, string(data.Kind)), title, data))
}

func (h *handlers) createItem(w http.ResponseWriter, r *http.Request) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.OneOf("kind", string(db.MediaKindGame), string(db.MediaKindBook), string(db.MediaKindFilm), string(db.MediaKindShow))
	kind := kindParam(f.Get("kind"))
	in := parseItemForm(f, kind)
	if !f.Valid() {
		h.renderItemForm(w, r, http.StatusUnprocessableEntity, itemFormData{Kind: kind, Form: f})
		return
	}
	var item db.MediaItem
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		item, err = CreateItem(r.Context(), q, viewer(r).ID, in)
		return err
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, item.Title+" was added to the catalog.")
	web.Redirect(w, r, itemURL(item))
}

func (h *handlers) editItem(w http.ResponseWriter, r *http.Request, item db.MediaItem) {
	in, err := LoadItemInput(r.Context(), h.d.Q, item)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	h.renderItemForm(w, r, http.StatusOK, itemFormData{Kind: item.Kind, Item: &item, Form: form.New(in.Values())})
}

func (h *handlers) updateItem(w http.ResponseWriter, r *http.Request, item db.MediaItem) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in := parseItemForm(f, item.Kind)
	if !f.Valid() {
		h.renderItemForm(w, r, http.StatusUnprocessableEntity, itemFormData{Kind: item.Kind, Item: &item, Form: f})
		return
	}
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		item, err = UpdateItem(r.Context(), q, item, in)
		return err
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Saved.")
	web.Redirect(w, r, itemURL(item))
}

func (h *handlers) renderEditionForm(w http.ResponseWriter, r *http.Request, status int, data editionFormData) {
	if data.Item.Kind == db.MediaKindGame {
		platforms, err := PlatformMatches(r.Context(), h.d.Q, data.Form.Get("platform_q"))
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		data.Platforms = platforms
	}
	title := "Add edition"
	if data.Edition != nil {
		title = "Edit edition"
	}
	web.Render(w, r, status, editionFormPage(web.PageFor(r, h.d, title, string(data.Item.Kind)), title, data))
}

func (h *handlers) newEdition(w http.ResponseWriter, r *http.Request, item db.MediaItem) {
	h.renderEditionForm(w, r, http.StatusOK, editionFormData{Item: item, Form: form.New(nil)})
}

func (h *handlers) editEdition(w http.ResponseWriter, r *http.Request, item db.MediaItem, e db.Edition) {
	h.renderEditionForm(w, r, http.StatusOK, editionFormData{Item: item, Edition: &e, Form: form.New(editionValues(e))})
}

// parseEdition validates the form and resolves the platform from platform_id, or from
// the platform search text when no option was chosen (the no-JS path).
func (h *handlers) parseEdition(r *http.Request, f *form.Form, item db.MediaItem) (EditionInput, error) {
	in := parseEditionForm(f, item.Kind)
	if item.Kind != db.MediaKindGame {
		return in, nil
	}
	var p db.Platform
	var err error
	switch {
	case f.Get("platform_id") != "":
		id, perr := strconv.ParseInt(f.Get("platform_id"), 10, 64)
		if perr != nil {
			f.Add("platform_id", "Unknown platform.")
			return in, nil
		}
		p, err = h.d.Q.GetPlatformByID(r.Context(), id)
	case f.Get("platform_q") != "":
		p, err = h.d.Q.FindPlatformByName(r.Context(), f.Get("platform_q"))
	default:
		return in, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		f.Add("platform_id", "Unknown platform.")
		return in, nil
	}
	if err != nil {
		return in, err
	}
	in.PlatformID = &p.ID
	return in, nil
}

func (h *handlers) createEdition(w http.ResponseWriter, r *http.Request, item db.MediaItem) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in, err := h.parseEdition(r, f, item)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if !f.Valid() {
		h.renderEditionForm(w, r, http.StatusUnprocessableEntity, editionFormData{Item: item, Form: f})
		return
	}
	if _, err := CreateEdition(r.Context(), h.d.Q, item.ID, in); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Edition added.")
	web.Redirect(w, r, itemURL(item))
}

func (h *handlers) updateEdition(w http.ResponseWriter, r *http.Request, item db.MediaItem, e db.Edition) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in, err := h.parseEdition(r, f, item)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if !f.Valid() {
		h.renderEditionForm(w, r, http.StatusUnprocessableEntity, editionFormData{Item: item, Edition: &e, Form: f})
		return
	}
	if _, err := UpdateEdition(r.Context(), h.d.Q, e.ID, in); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Edition saved.")
	web.Redirect(w, r, itemURL(item))
}

func (h *handlers) deleteEdition(w http.ResponseWriter, r *http.Request, item db.MediaItem, e db.Edition) {
	err := h.d.Q.CatalogDeleteEdition(r.Context(), e.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		web.Flash(r.Context(), h.d, "That edition is in someone's library or journal, so it can't be deleted.")
		web.Redirect(w, r, itemURL(item))
		return
	}
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Edition deleted.")
	web.Redirect(w, r, itemURL(item))
}

// platformPicker returns the platform <select> filtered by platform_q (htmx).
func (h *handlers) platformPicker(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("platform_q")
	platforms, err := PlatformMatches(r.Context(), h.d.Q, q)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	selected := r.URL.Query().Get("platform_id")
	if q != "" && len(platforms) > 0 {
		selected = strconv.FormatInt(platforms[0].ID, 10)
	}
	web.Render(w, r, http.StatusOK, platformSelect(platforms, selected, ""))
}

type searchGroup struct {
	Kind  db.MediaKind
	Items []db.CatalogSearchRow
}

// groupResults groups by kind, ordering groups by their best-ranked result.
func groupResults(rows []db.CatalogSearchRow) []searchGroup {
	var groups []searchGroup
	idx := map[db.MediaKind]int{}
	for _, row := range rows {
		i, ok := idx[row.Kind]
		if !ok {
			i = len(groups)
			idx[row.Kind] = i
			groups = append(groups, searchGroup{Kind: row.Kind})
		}
		groups[i].Items = append(groups[i].Items, row)
	}
	return groups
}

func (h *handlers) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kindStr := r.URL.Query().Get("kind")
	var kind *db.MediaKind
	if k := db.MediaKind(kindStr); k.Valid() {
		kind = &k
	} else {
		kindStr = ""
	}
	data := searchData{Query: q, Kind: kindStr}
	if rs := []rune(q); len(rs) > 200 {
		q = string(rs[:200])
	}
	if q != "" {
		rows, err := h.d.Q.CatalogSearch(r.Context(), db.CatalogSearchParams{Query: q, Kind: kind, Lim: 60})
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		data.Groups = groupResults(rows)
	}
	if web.IsPartial(r) {
		web.Render(w, r, http.StatusOK, searchResults(data))
		return
	}
	web.Render(w, r, http.StatusOK, searchPage(web.PageFor(r, h.d, "Search", ""), data))
}
