package images

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
)

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.HandleFunc("GET /covers/{id}/{size}", func(w http.ResponseWriter, r *http.Request) { serveCover(w, r, d) })
	mux.Handle("GET /items/{id}/cover", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { editCover(w, r, d) })))
	mux.Handle("POST /items/{id}/cover", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { uploadCover(w, r, d) })))
}

func serveCover(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	size := Size(r.PathValue("size"))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !size.Valid() {
		http.NotFound(w, r)
		return
	}
	img, err := d.Q.GetImage(r.Context(), id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		d.Log.Error("load image", "id", id, "err", err)
	}
	if err == nil && img.Status == db.ImageStatusReady && img.Sha256 != nil && len(*img.Sha256) == 64 {
		if f, err := os.Open(FilePath(d.Config.DataDir, *img.Sha256, size)); err == nil {
			defer func() { _ = f.Close() }()
			if st, err := f.Stat(); err == nil {
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				http.ServeContent(w, r, "", st.ModTime(), f)
				return
			}
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	http.Redirect(w, r, PlaceholderURL(db.MediaKind(r.URL.Query().Get("k"))), http.StatusFound)
}

// loadEditableItem returns the item if the viewer is an admin or its creator.
func loadEditableItem(w http.ResponseWriter, r *http.Request, d *deps.Deps) (db.MediaItem, bool) {
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
	viewer, _ := auth.CurrentUser(r.Context())
	if viewer.Role != db.UserRoleAdmin && (item.CreatedByUserID == nil || *item.CreatedByUserID != viewer.ID) {
		http.Error(w, "Only admins or the item's creator can change its cover.", http.StatusForbidden)
		return db.MediaItem{}, false
	}
	return item, true
}

func editCover(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	item, ok := loadEditableItem(w, r, d)
	if !ok {
		return
	}
	web.Render(w, r, http.StatusOK, coverPage(web.PageFor(r, d, "Cover for "+item.Title, string(item.Kind)), item, ""))
}

func uploadCover(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	item, ok := loadEditableItem(w, r, d)
	if !ok {
		return
	}
	fail := func(msg string) {
		web.Render(w, r, http.StatusUnprocessableEntity, coverPage(web.PageFor(r, d, "Cover for "+item.Title, string(item.Kind)), item, msg))
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBytes+1<<20)
	if err := r.ParseMultipartForm(MaxBytes); err != nil {
		fail("Upload an image file under 10 MB.")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, _, err := r.FormFile("cover")
	if err != nil {
		fail("Choose an image file.")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	p, err := Process(d.Config.DataDir, data)
	if err != nil {
		var pe *processError
		if errors.As(err, &pe) {
			fail("That file isn't a supported image (JPEG, PNG, GIF, or WebP up to 10 MB and 40 megapixels).")
			return
		}
		web.Error(w, r, d, err)
		return
	}
	err = d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		now := d.Now()
		width, height := int32(p.Width), int32(p.Height)
		imgID, err := q.ImagesInsertUpload(r.Context(), db.ImagesInsertUploadParams{Sha256: &p.SHA256, ContentType: &p.ContentType, Width: &width, Height: &height, FetchedAt: &now})
		if err != nil {
			return err
		}
		return q.ImagesSetItemCover(r.Context(), db.ImagesSetItemCoverParams{ID: item.ID, ImageID: &imgID})
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Cover updated.")
	web.Redirect(w, r, "/items/"+item.PublicID.String())
}
