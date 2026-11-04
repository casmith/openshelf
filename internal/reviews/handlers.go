package reviews

import (
	"context"
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const maxBody = 20000

func Register(mux *http.ServeMux, d *deps.Deps) {
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) {
		mux.Handle(pattern, auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) })))
	}
	handle("GET /items/{id}/review", editReview)
	handle("POST /items/{id}/review", saveReview)
	handle("GET /reviews/{id}", showReview)
	handle("DELETE /reviews/{id}", deleteReview)
	// No-JS fallback for the delete button.
	handle("POST /reviews/{id}/delete", deleteReview)
}

// ItemPanel renders the viewer's review and others' visible reviews on the item page.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	own, err := d.Q.ReviewsGetOwnForItem(ctx, db.ReviewsGetOwnForItemParams{UserID: viewer.ID, MediaItemID: item.ID})
	var ownPtr *db.Review
	switch {
	case err == nil:
		ownPtr = &own
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	others, err := d.Q.ReviewsListOthersForItem(ctx, db.ReviewsListOthersForItemParams{MediaItemID: item.ID, ViewerID: viewer.ID})
	if err != nil {
		return nil, err
	}
	return panel(viewer.RatingDisplay, item, ownPtr, others), nil
}

func loadItem(w http.ResponseWriter, r *http.Request, d *deps.Deps) (db.MediaItem, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return db.MediaItem{}, false
	}
	item, err := d.Q.GetMediaItemByPublicID(r.Context(), id)
	if err != nil {
		web.Error(w, r, d, err)
		return db.MediaItem{}, false
	}
	return item, true
}

type formData struct {
	Item        db.MediaItem
	Display     db.RatingDisplay
	Form        *form.Form
	Rating      *int16
	Experiences []db.ReviewsListOwnExperiencesForItemRow
	Existing    *db.Review
}

func editReview(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	item, ok := loadItem(w, r, d)
	if !ok {
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	fd := formData{Item: item, Display: viewer.RatingDisplay, Form: form.New(nil)}
	own, err := d.Q.ReviewsGetOwnForItem(r.Context(), db.ReviewsGetOwnForItemParams{UserID: viewer.ID, MediaItemID: item.ID})
	switch {
	case err == nil:
		fd.Existing = &own
		fd.Rating = own.Rating
		if own.Body != nil {
			fd.Form.Values.Set("body", *own.Body)
		}
		if own.ContainsSpoilers {
			fd.Form.Values.Set("spoilers", "on")
		}
		fd.Form.Values.Set("visibility", string(own.Visibility))
	case errors.Is(err, pgx.ErrNoRows):
		fd.Form.Values.Set("visibility", string(viewer.DefaultVisibility))
	default:
		web.Error(w, r, d, err)
		return
	}
	renderForm(w, r, d, http.StatusOK, fd, own.ExperienceID)
}

// renderForm loads the viewer's experiences and renders the form; expID preselects one.
func renderForm(w http.ResponseWriter, r *http.Request, d *deps.Deps, status int, fd formData, expID *int64) {
	viewer, _ := auth.CurrentUser(r.Context())
	exps, err := d.Q.ReviewsListOwnExperiencesForItem(r.Context(), db.ReviewsListOwnExperiencesForItemParams{UserID: viewer.ID, MediaItemID: fd.Item.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	fd.Experiences = exps
	if expID != nil {
		for _, e := range exps {
			if e.ID == *expID {
				fd.Form.Values.Set("experience", e.PublicID.String())
			}
		}
	}
	web.Render(w, r, status, formPage(web.PageFor(r, d, "Review "+fd.Item.Title, string(fd.Item.Kind)), fd))
}

func saveReview(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	item, ok := loadItem(w, r, d)
	if !ok {
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	f, err := form.Parse(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in := SaveInput{Body: f.Values.Get("body"), Spoilers: f.Bool("spoilers"), Visibility: db.Visibility(f.Get("visibility"))}
	in.Rating, err = ParseRating(f.Get("rating"))
	if err != nil {
		f.Add("rating", "Rating must be a whole number from 0 to 100.")
	}
	f.MaxLen("body", maxBody)
	if !in.Visibility.Valid() {
		f.Add("visibility", "Invalid choice.")
	}
	if v := f.Get("experience"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			f.Add("experience", "Invalid choice.")
		} else {
			in.ExperienceID = &id
		}
	}
	var rev db.Review
	if f.Valid() {
		err = d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
			rev, err = Save(r.Context(), q, viewer.ID, item.ID, in, d.Now())
			return err
		})
		switch {
		case errors.Is(err, ErrEmpty):
			f.Add("_form", "Add a rating, a review, or both.")
		case errors.Is(err, ErrExperienceNotFound):
			f.Add("experience", "Invalid choice.")
		case err != nil:
			web.Error(w, r, d, err)
			return
		}
	}
	if !f.Valid() {
		renderForm(w, r, d, http.StatusUnprocessableEntity, formData{Item: item, Display: viewer.RatingDisplay, Form: f, Rating: in.Rating}, nil)
		return
	}
	web.Flash(r.Context(), d, "Review saved.")
	web.Redirect(w, r, "/reviews/"+rev.PublicID.String())
}

func showReview(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	var viewerID *int64
	viewer, ok := auth.CurrentUser(r.Context())
	if ok {
		viewerID = &viewer.ID
	}
	row, err := d.Q.ReviewsGetVisible(r.Context(), db.ReviewsGetVisibleParams{ViewerID: viewerID, PublicID: id})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	display := db.RatingDisplayStars5
	if ok {
		display = viewer.RatingDisplay
	}
	owner := ok && row.Review.UserID == viewer.ID
	web.Render(w, r, http.StatusOK, showPage(web.PageFor(r, d, row.DisplayName+" on "+row.ItemTitle, string(row.ItemKind)), row, display, owner))
}

func deleteReview(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	itemID, err := d.Q.ReviewsDeleteOwn(r.Context(), db.ReviewsDeleteOwnParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Review deleted.")
	web.Redirect(w, r, "/items/"+itemID.String())
}
