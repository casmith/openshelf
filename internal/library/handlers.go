package library

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/suggestions"
	"github.com/casmith/openshelf/internal/synchistory"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const viewSessionKey = "library_view"

// Register adds this package's routes.
func Register(mux *http.ServeMux, d *deps.Deps) {
	h := func(fn func(http.ResponseWriter, *http.Request, *deps.Deps, db.User)) http.Handler {
		return auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := auth.CurrentUser(r.Context())
			fn(w, r, d, u)
		}))
	}
	mux.Handle("GET /library", h(index))
	mux.Handle("GET /library/{kind}", h(index))
	mux.Handle("POST /items/{id}/library", h(add))
	mux.Handle("GET /library/{entry_id}/edit", h(edit))
	mux.Handle("POST /library/{entry_id}", h(update))
	mux.Handle("DELETE /library/{entry_id}", h(remove))
	mux.Handle("POST /library/{entry_id}/delete", h(remove))
	mux.Handle("POST /library/{entry_id}/played", h(setPlayed))
	mux.Handle("POST /library/{entry_id}/ownership", h(setOwnership))
}

func index(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	var kind *db.MediaKind
	nav := "library"
	if k := r.PathValue("kind"); k != "" {
		mk := db.MediaKind(k)
		if !mk.Valid() {
			web.NotFound(w, r, d)
			return
		}
		kind, nav = &mk, k
	}
	f := ParseFilter(kind, r.URL.Query())
	if f.View != "" {
		if d.Sessions.GetString(r.Context(), viewSessionKey) != f.View {
			d.Sessions.Put(r.Context(), viewSessionKey, f.View)
		}
	} else {
		f.View = d.Sessions.GetString(r.Context(), viewSessionKey)
		if f.View == "" {
			f.View = "grid"
		}
	}
	page, err := List(r.Context(), d.Q, viewer, f)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	facets, err := LoadFacets(r.Context(), d.Q, viewer, kind)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	src, err := loadSources(r.Context(), d, viewer, page.Entries)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	v := libraryView{Path: r.URL.Path, Filter: f, Page: page, Facets: facets, Display: viewer.RatingDisplay}
	if web.IsPartial(r) {
		web.Render(w, r, http.StatusOK, libraryBody(v, src))
		return
	}
	if kind == nil || *kind == db.MediaKindGame || *kind == db.MediaKindBook {
		if v.Suggested, err = suggestions.Count(r.Context(), d, viewer); err != nil {
			web.Error(w, r, d, err)
			return
		}
		if kind != nil {
			v.Suggested = v.Suggested.Only(*kind)
		}
	}
	web.Render(w, r, http.StatusOK, libraryPage(web.PageFor(r, d, libraryTitle(kind), nav), v, src))
}

// parseEntry validates the shared entry fields.
func parseEntry(f *form.Form) EntryInput {
	in := EntryInput{Backlog: f.Bool("backlog"), Visibility: db.Visibility(f.Get("visibility"))}
	f.OneOf("ownership", string(db.OwnershipOwned), string(db.OwnershipBorrowed), string(db.OwnershipNotOwned))
	if o := db.Ownership(f.Get("ownership")); o.Valid() {
		in.Ownership = &o
	}
	if !in.Visibility.Valid() {
		f.Add("visibility", "Invalid choice.")
	}
	in.AcquiredOn = f.Date("acquired_on")
	f.MaxLen("notes", maxNotesLen)
	in.Notes = f.Optional("notes")
	in.Tags = NormalizeTags(f.Get("tags"))
	if len(in.Tags) > maxTags {
		f.Add("tags", "Use at most "+strconv.Itoa(maxTags)+" tags.")
	}
	for _, t := range in.Tags {
		if len([]rune(t)) > maxTagLen {
			f.Add("tags", "Each tag must be at most "+strconv.Itoa(maxTagLen)+" characters.")
		}
	}
	return in
}

func add(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	itemID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	item, err := d.Q.GetMediaItemByPublicID(r.Context(), itemID)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if f.Get("visibility") == "" {
		f.Values.Set("visibility", string(viewer.DefaultVisibility))
	}
	in := parseEntry(f)
	var editionID *int64
	if e := f.Get("edition"); e != "" {
		eid, err := uuid.Parse(e)
		if err == nil {
			id, qerr := d.Q.LibraryEditionIDForItem(r.Context(), db.LibraryEditionIDForItemParams{PublicID: eid, MediaItemID: item.ID})
			if qerr == nil {
				editionID = &id
			}
			err = qerr
		}
		if err != nil {
			f.Add("edition", "Choose an edition of this item.")
		}
	}
	if f.Valid() {
		_, err = Add(r.Context(), d, viewer, item, editionID, in)
		if errors.Is(err, ErrDuplicate) {
			f.Add("_form", "This is already in your library. Choose a different edition to add another copy.")
		} else if err != nil {
			web.Error(w, r, d, err)
			return
		}
	}
	if !f.Valid() {
		editions, err := d.Q.ListEditionsForItem(r.Context(), item.ID)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		web.Render(w, r, http.StatusUnprocessableEntity, addPage(web.PageFor(r, d, "Add to library", string(item.Kind)), item, editions, f))
		return
	}
	web.Flash(r.Context(), d, "Added to your library.")
	web.Redirect(w, r, itemURL(item.PublicID))
}

func entryID(w http.ResponseWriter, r *http.Request, d *deps.Deps) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("entry_id"))
	if err != nil {
		web.NotFound(w, r, d)
		return uuid.Nil, false
	}
	return id, true
}

func edit(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	id, ok := entryID(w, r, d)
	if !ok {
		return
	}
	entry, err := d.Q.LibraryGetEntryForEdit(r.Context(), db.LibraryGetEntryForEditParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Render(w, r, http.StatusOK, editPage(web.PageFor(r, d, "Edit library entry", string(entry.ItemKind)), entry, entryFormValues(entry)))
}

func update(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	id, ok := entryID(w, r, d)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in := parseEntry(f)
	if !f.Valid() {
		entry, err := d.Q.LibraryGetEntryForEdit(r.Context(), db.LibraryGetEntryForEditParams{PublicID: id, UserID: viewer.ID})
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		web.Render(w, r, http.StatusUnprocessableEntity, editPage(web.PageFor(r, d, "Edit library entry", string(entry.ItemKind)), entry, f))
		return
	}
	itemPublicID, err := d.Q.LibraryUpdateEntry(r.Context(), db.LibraryUpdateEntryParams{
		EntryID:       id,
		UserID:        viewer.ID,
		OwnershipUser: in.Ownership,
		Backlog:       in.Backlog,
		AcquiredOn:    in.AcquiredOn,
		Tags:          in.Tags,
		Notes:         in.Notes,
		Visibility:    in.Visibility,
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Library entry saved.")
	web.Redirect(w, r, itemURL(itemPublicID))
}

func remove(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	id, ok := entryID(w, r, d)
	if !ok {
		return
	}
	var itemPublicID uuid.UUID
	err := d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		var err error
		if itemPublicID, err = q.LibraryDeleteEntry(r.Context(), db.LibraryDeleteEntryParams{EntryID: id, UserID: viewer.ID}); err != nil {
			return err
		}
		return synchistory.ExcludeRemovedItem(r.Context(), q, viewer, itemPublicID)
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Removed from your library.")
	web.Redirect(w, r, itemURL(itemPublicID))
}

// setPlayed writes only played_user (ADR 0002); "reset" clears the override.
func setPlayed(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	id, ok := entryID(w, r, d)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var played *bool
	switch f.Get("played") {
	case "true":
		v := true
		played = &v
	case "false":
		v := false
		played = &v
	case "reset":
	default:
		http.Error(w, "played must be true, false, or reset", http.StatusUnprocessableEntity)
		return
	}
	itemPublicID, err := d.Q.LibrarySetPlayed(r.Context(), db.LibrarySetPlayedParams{EntryID: id, UserID: viewer.ID, PlayedUser: played})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Redirect(w, r, itemURL(itemPublicID))
}

// setOwnership writes only ownership_user; "reset" clears it so the source value applies.
func setOwnership(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	id, ok := entryID(w, r, d)
	if !ok {
		return
	}
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var own *db.Ownership
	if v := f.Get("ownership"); v != "reset" {
		o := db.Ownership(v)
		if !o.Valid() {
			http.Error(w, "ownership must be owned, borrowed, not_owned, or reset", http.StatusUnprocessableEntity)
			return
		}
		own = &o
	}
	itemPublicID, err := d.Q.LibrarySetOwnership(r.Context(), db.LibrarySetOwnershipParams{EntryID: id, UserID: viewer.ID, OwnershipUser: own})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Redirect(w, r, itemURL(itemPublicID))
}

func itemURL(id uuid.UUID) string { return "/items/" + id.String() }
