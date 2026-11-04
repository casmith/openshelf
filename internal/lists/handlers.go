package lists

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const (
	maxNameLen        = 200
	maxDescriptionLen = 5000
)

type handlers struct{ d *deps.Deps }

// Register adds the /lists routes. List pages are readable without login when public.
func Register(mux *http.ServeMux, d *deps.Deps) {
	h := &handlers{d: d}
	user := func(f http.HandlerFunc) http.Handler { return auth.RequireUser(f) }
	mux.Handle("GET /lists", user(h.index))
	mux.Handle("GET /lists/new", user(h.newForm))
	mux.Handle("POST /lists", user(h.create))
	mux.HandleFunc("GET /lists/{id}", h.show)
	mux.Handle("GET /lists/{id}/edit", user(h.editForm))
	mux.Handle("POST /lists/{id}/edit", user(h.update))
	mux.Handle("DELETE /lists/{id}", user(h.delete))
	mux.Handle("POST /lists/{id}/delete", user(h.delete))
	mux.Handle("POST /lists/{id}/items", user(h.addItem))
	mux.Handle("POST /lists/{id}/items/{item}/move", user(h.moveItem))
	mux.Handle("DELETE /lists/{id}/items/{item}", user(h.removeItem))
	mux.Handle("POST /lists/{id}/items/{item}/delete", user(h.removeItem))
}

func viewer(r *http.Request) db.User {
	u, _ := auth.CurrentUser(r.Context())
	return u
}

func pathID(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	return id, err == nil
}

func listURL(id uuid.UUID) string { return "/lists/" + id.String() }

func (h *handlers) index(w http.ResponseWriter, r *http.Request) {
	v := viewer(r)
	shared := r.URL.Query().Get("tab") == "shared"
	var cards []listCard
	if shared {
		rows, err := h.d.Q.ListsShared(r.Context(), v.ID)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		for _, l := range rows {
			cards = append(cards, listCard{ID: l.ID, PublicID: l.PublicID, Name: l.Name, Owner: l.OwnerDisplayName,
				Count: l.ItemCount, Ranked: l.Ranked, Visibility: l.Visibility})
		}
	} else {
		rows, err := h.d.Q.ListsForOwner(r.Context(), v.ID)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		for _, l := range rows {
			cards = append(cards, listCard{ID: l.ID, PublicID: l.PublicID, Name: l.Name,
				Count: l.ItemCount, Ranked: l.Ranked, Visibility: l.Visibility})
		}
	}
	if len(cards) > 0 {
		ids := make([]int64, len(cards))
		for i, c := range cards {
			ids[i] = c.ID
		}
		covers, err := h.d.Q.ListsCollageCovers(r.Context(), ids)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		byList := map[int64][]string{}
		for _, c := range covers {
			byList[c.ListID] = append(byList[c.ListID], coverThumb(c.CoverImageID, c.Kind))
		}
		for i := range cards {
			cards[i].Covers = byList[cards[i].ID]
		}
	}
	web.Render(w, r, http.StatusOK, indexPage(web.PageFor(r, h.d, "Lists", "lists"), shared, cards))
}

func validate(f *form.Form) {
	f.Required("name", "visibility")
	f.MaxLen("name", maxNameLen)
	f.MaxLen("description", maxDescriptionLen)
	f.OneOf("visibility", string(db.VisibilityPrivate), string(db.VisibilityInstance), string(db.VisibilityPublic))
}

func (h *handlers) newForm(w http.ResponseWriter, r *http.Request) {
	f := form.New(nil)
	f.Values.Set("visibility", string(viewer(r).DefaultVisibility))
	if item, err := uuid.Parse(r.URL.Query().Get("item")); err == nil {
		f.Values.Set("item", item.String())
	}
	web.Render(w, r, http.StatusOK, newPage(web.PageFor(r, h.d, "New list", "lists"), f))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	validate(f)
	var itemID *uuid.UUID
	if raw := f.Get("item"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			f.Add("_form", "That item doesn't exist.")
		}
		itemID = &id
	}
	if !f.Valid() {
		web.Render(w, r, http.StatusUnprocessableEntity, newPage(web.PageFor(r, h.d, "New list", "lists"), f))
		return
	}
	v := viewer(r)
	var list db.List
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		var err error
		list, err = q.ListsCreate(r.Context(), db.ListsCreateParams{
			UserID:      v.ID,
			Name:        f.Get("name"),
			Description: f.Optional("description"),
			Ranked:      f.Bool("ranked"),
			Visibility:  db.Visibility(f.Get("visibility")),
		})
		if err != nil || itemID == nil {
			return err
		}
		item, err := q.GetMediaItemByPublicID(r.Context(), *itemID)
		if err != nil {
			return err
		}
		_, err = AddItem(r.Context(), q, list, item.ID)
		return err
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "List created.")
	web.Redirect(w, r, listURL(list.PublicID))
}

func (h *handlers) show(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		web.NotFound(w, r, h.d)
		return
	}
	v := viewer(r)
	list, err := h.d.Q.ListsGetVisible(r.Context(), db.ListsGetVisibleParams{PublicID: id, ViewerID: v.ID})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	items, err := h.d.Q.ListsItems(r.Context(), list.ID)
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	data := listData{List: list, Items: items, Owner: v.ID != 0 && list.UserID == v.ID}
	web.Render(w, r, http.StatusOK, showPage(web.PageFor(r, h.d, list.Name, "lists"), data))
}

func (h *handlers) owned(w http.ResponseWriter, r *http.Request) (db.List, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		web.NotFound(w, r, h.d)
		return db.List{}, false
	}
	list, err := h.d.Q.ListsGetOwned(r.Context(), db.ListsGetOwnedParams{PublicID: id, UserID: viewer(r).ID})
	if err != nil {
		web.Error(w, r, h.d, err)
		return db.List{}, false
	}
	return list, true
}

func (h *handlers) editForm(w http.ResponseWriter, r *http.Request) {
	list, ok := h.owned(w, r)
	if !ok {
		return
	}
	f := form.New(nil)
	f.Values.Set("name", list.Name)
	if list.Description != nil {
		f.Values.Set("description", *list.Description)
	}
	if list.Ranked {
		f.Values.Set("ranked", "on")
	}
	f.Values.Set("visibility", string(list.Visibility))
	web.Render(w, r, http.StatusOK, editPage(web.PageFor(r, h.d, "Edit "+list.Name, "lists"), list, f))
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	list, ok := h.owned(w, r)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	validate(f)
	if !f.Valid() {
		web.Render(w, r, http.StatusUnprocessableEntity, editPage(web.PageFor(r, h.d, "Edit "+list.Name, "lists"), list, f))
		return
	}
	list, err = h.d.Q.ListsUpdate(r.Context(), db.ListsUpdateParams{
		ID:          list.ID,
		UserID:      list.UserID,
		Name:        f.Get("name"),
		Description: f.Optional("description"),
		Ranked:      f.Bool("ranked"),
		Visibility:  db.Visibility(f.Get("visibility")),
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "List saved.")
	web.Redirect(w, r, listURL(list.PublicID))
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	list, ok := h.owned(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Q.ListsDelete(r.Context(), db.ListsDeleteParams{ID: list.ID, UserID: list.UserID}); err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	web.Flash(r.Context(), h.d, "Deleted “"+list.Name+"”.")
	web.Redirect(w, r, "/lists")
}

func (h *handlers) addItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		web.NotFound(w, r, h.d)
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	itemID, err := uuid.Parse(f.Get("item"))
	if err != nil {
		web.NotFound(w, r, h.d)
		return
	}
	v := viewer(r)
	var (
		list  db.List
		item  db.MediaItem
		added bool
	)
	err = h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		var err error
		if list, err = q.ListsLockOwned(r.Context(), db.ListsLockOwnedParams{PublicID: id, UserID: v.ID}); err != nil {
			return err
		}
		if item, err = q.GetMediaItemByPublicID(r.Context(), itemID); err != nil {
			return err
		}
		added, err = AddItem(r.Context(), q, list, item.ID)
		return err
	})
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if added {
		web.Flash(r.Context(), h.d, "Added “"+item.Title+"” to “"+list.Name+"”.")
	} else {
		web.Flash(r.Context(), h.d, "“"+item.Title+"” is already on “"+list.Name+"”.")
	}
	next := listURL(list.PublicID)
	if f.Get("next") != "" {
		next = auth.SafeNext(f.Get("next"))
	}
	web.Redirect(w, r, next)
}

// itemOp locks the viewer's list, resolves {item}, and runs fn in one transaction.
// Partial requests get the refreshed items fragment; others are redirected to the list.
func (h *handlers) itemOp(w http.ResponseWriter, r *http.Request, fn func(q *db.Queries, list db.List, item db.MediaItem) error, flash func(item db.MediaItem) string) {
	id, ok1 := pathID(r, "id")
	itemID, ok2 := pathID(r, "item")
	if !ok1 || !ok2 {
		web.NotFound(w, r, h.d)
		return
	}
	v := viewer(r)
	var (
		list db.List
		item db.MediaItem
	)
	err := h.d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		var err error
		if list, err = q.ListsLockOwned(r.Context(), db.ListsLockOwnedParams{PublicID: id, UserID: v.ID}); err != nil {
			return err
		}
		if item, err = q.GetMediaItemByPublicID(r.Context(), itemID); err != nil {
			return err
		}
		return fn(q, list, item)
	})
	if errors.Is(err, ErrBadMove) {
		http.Error(w, "invalid move", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		web.Error(w, r, h.d, err)
		return
	}
	if web.IsPartial(r) {
		items, err := h.d.Q.ListsItems(r.Context(), list.ID)
		if err != nil {
			web.Error(w, r, h.d, err)
			return
		}
		web.Render(w, r, http.StatusOK, itemsSection(list.PublicID, list.Ranked, true, items))
		return
	}
	if flash != nil {
		web.Flash(r.Context(), h.d, flash(item))
	}
	web.Redirect(w, r, listURL(list.PublicID))
}

func (h *handlers) moveItem(w http.ResponseWriter, r *http.Request) {
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.itemOp(w, r, func(q *db.Queries, list db.List, item db.MediaItem) error {
		return MoveItem(r.Context(), q, list, item.ID, f.Get("to"))
	}, nil)
}

func (h *handlers) removeItem(w http.ResponseWriter, r *http.Request) {
	h.itemOp(w, r, func(q *db.Queries, list db.List, item db.MediaItem) error {
		return RemoveItem(r.Context(), q, list, item.ID)
	}, func(item db.MediaItem) string { return "Removed “" + item.Title + "”." })
}
