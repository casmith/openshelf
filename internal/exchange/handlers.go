package exchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

func Register(mux *http.ServeMux, d *deps.Deps) {
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request, *deps.Deps)) {
		mux.Handle(pattern, auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w, r, d) })))
	}
	handle("GET /settings/export", showExport)
	handle("GET /settings/export.json", downloadExport)
	handle("GET /settings/import", showImport)
	handle("POST /settings/import", uploadImport)
	handle("GET /settings/import/{id}", showJob)
	handle("POST /settings/import/{id}/confirm", confirmJob)
}

func showExport(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	web.Render(w, r, http.StatusOK, exportPage(web.PageFor(r, d, "Export", "")))
}

func downloadExport(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	viewer, _ := auth.CurrentUser(r.Context())
	now := d.Now()
	name := "openshelf-" + viewer.Username + "-" + now.Format("2006-01-02") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := WriteExport(r.Context(), d.Q, viewer, now, w); err != nil {
		d.Log.Error("export failed", "user", viewer.ID, "err", err)
	}
}

func renderImport(w http.ResponseWriter, r *http.Request, d *deps.Deps, status int, f *form.Form) {
	viewer, _ := auth.CurrentUser(r.Context())
	jobs, err := d.Q.ExchangeListJobs(r.Context(), viewer.ID)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Render(w, r, status, importPage(web.PageFor(r, d, "Import", ""), f, jobs))
}

func showImport(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	renderImport(w, r, d, http.StatusOK, form.New(nil))
}

// detectFormat uses the file extension, falling back to sniffing for a JSON object.
func detectFormat(name string, content []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return "json"
	case ".csv":
		return "csv"
	}
	if bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))), []byte("{")) {
		return "json"
	}
	return "csv"
}

func uploadImport(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	viewer, _ := auth.CurrentUser(r.Context())
	f := form.New(nil)
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			f.Add("file", "The file must be 10 MB or smaller.")
			renderImport(w, r, d, http.StatusUnprocessableEntity, f)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.Values = r.PostForm
	preview := f.Bool("preview")
	file, header, err := r.FormFile("file")
	if err != nil {
		f.Add("file", "Choose a file to import.")
		renderImport(w, r, d, http.StatusUnprocessableEntity, f)
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxUploadBytes+1))
	switch {
	case err != nil:
		web.Error(w, r, d, err)
		return
	case len(content) > MaxUploadBytes:
		f.Add("file", "The file must be 10 MB or smaller.")
	case len(bytes.TrimSpace(content)) == 0:
		f.Add("file", "The file is empty.")
	}
	if !f.Valid() {
		renderImport(w, r, d, http.StatusUnprocessableEntity, f)
		return
	}
	filename := filepath.Base(header.Filename)
	if len(filename) > 200 {
		filename = filename[:200]
	}
	status := StatusQueued
	if preview {
		status = StatusPreviewing
	}
	var job db.ExchangeInsertJobRow
	err = d.WithTx(r.Context(), func(q *db.Queries, tx pgx.Tx) error {
		job, err = q.ExchangeInsertJob(r.Context(), db.ExchangeInsertJobParams{
			UserID: viewer.ID, Format: detectFormat(filename, content), Filename: filename, Content: content, Status: status,
		})
		if err != nil {
			return err
		}
		_, err = d.River.InsertTx(r.Context(), tx, ImportArgs{JobID: job.ID, DryRun: preview}, nil)
		return err
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Redirect(w, r, jobURL(job.PublicID.String()))
}

func loadJob(w http.ResponseWriter, r *http.Request, d *deps.Deps) (db.ExchangeGetJobRow, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return db.ExchangeGetJobRow{}, false
	}
	viewer, _ := auth.CurrentUser(r.Context())
	job, err := d.Q.ExchangeGetJob(r.Context(), db.ExchangeGetJobParams{PublicID: id, UserID: viewer.ID})
	if err != nil {
		web.Error(w, r, d, err)
		return job, false
	}
	return job, true
}

func showJob(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	job, ok := loadJob(w, r, d)
	if !ok {
		return
	}
	var errs []RowError
	if err := json.Unmarshal(job.RowErrors, &errs); err != nil {
		web.Error(w, r, d, err)
		return
	}
	if web.IsPartial(r) {
		web.Render(w, r, http.StatusOK, jobStatus(job, errs))
		return
	}
	web.Render(w, r, http.StatusOK, jobPage(web.PageFor(r, d, "Import", ""), job, errs))
}

func confirmJob(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	err = d.WithTx(r.Context(), func(q *db.Queries, tx pgx.Tx) error {
		jobID, err := q.ExchangeQueueJob(r.Context(), db.ExchangeQueueJobParams{PublicID: id, UserID: viewer.ID})
		if err != nil {
			return err
		}
		_, err = d.River.InsertTx(r.Context(), tx, ImportArgs{JobID: jobID}, nil)
		return err
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Import started.")
	web.Redirect(w, r, jobURL(id.String()))
}
