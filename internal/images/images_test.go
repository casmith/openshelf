package images_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/images"
	"github.com/casmith/openshelf/internal/safehttp"
	"github.com/casmith/openshelf/internal/testutil"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func enqueue(t *testing.T, s *testutil.Server, url string) int64 {
	t.Helper()
	var id int64
	err := pgx.BeginFunc(context.Background(), s.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = images.Enqueue(context.Background(), s.Deps, tx, url)
		return err
	})
	if err != nil {
		t.Fatalf("enqueue %s: %v", url, err)
	}
	return id
}

func countJobs(t *testing.T, s *testutil.Server) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), "select count(*) from river_job where kind = 'fetch_image'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func work(t *testing.T, s *testutil.Server, client *safehttp.Client, id int64, attempt int) error {
	t.Helper()
	w := &images.FetchImageWorker{D: s.Deps, HTTP: client}
	return w.Work(context.Background(), &river.Job[images.FetchImageArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 3},
		Args:   images.FetchImageArgs{ImageID: id},
	})
}

func getImage(t *testing.T, s *testutil.Server, id int64) db.Image {
	t.Helper()
	img, err := s.Q.GetImage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func coverFiles(t *testing.T, s *testutil.Server) []string {
	t.Helper()
	var files []string
	root := filepath.Join(s.Deps.Config.DataDir, "covers")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	data := pngBytes(t, 600, 900)
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}
	mux.HandleFunc("/a.png", serve)
	mux.HandleFunc("/b.png", serve)
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html><body>hi</body></html>")) })
	mux.HandleFunc("/huge.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data[:8])
		_, _ = w.Write(make([]byte, 11<<20))
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "oops", http.StatusInternalServerError) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEnqueueInsertsOneJob(t *testing.T) {
	s := testutil.NewServer(t)
	id := enqueue(t, s, "https://example.com/a.jpg")
	if again := enqueue(t, s, "https://example.com/a.jpg"); again != id {
		t.Fatalf("second enqueue returned %d, want %d", again, id)
	}
	if n := countJobs(t, s); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}
	if getImage(t, s, id).Status != db.ImageStatusPending {
		t.Fatal("new image should be pending")
	}

	ready := enqueue(t, s, "https://example.com/b.jpg")
	if _, err := s.Pool.Exec(context.Background(), "delete from river_job"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(context.Background(), "update images set status = 'ready' where id = $1", ready); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "https://example.com/b.jpg")
	if n := countJobs(t, s); n != 0 {
		t.Fatalf("ready image enqueued a job (%d)", n)
	}
}

func TestFetchSharesFilesForIdenticalBytes(t *testing.T) {
	s := testutil.NewServer(t)
	srv := imageServer(t)
	client := safehttp.New(safehttp.Options{AllowLoopback: true})
	a := enqueue(t, s, srv.URL+"/a.png")
	b := enqueue(t, s, srv.URL+"/b.png")
	for _, id := range []int64{a, b} {
		if err := work(t, s, client, id, 1); err != nil {
			t.Fatal(err)
		}
	}
	ia, ib := getImage(t, s, a), getImage(t, s, b)
	if ia.Status != db.ImageStatusReady || ib.Status != db.ImageStatusReady {
		t.Fatalf("status %s/%s, error %v", ia.Status, ib.Status, ia.Error)
	}
	if *ia.Sha256 != *ib.Sha256 || *ia.Width != 600 || *ia.Height != 900 || *ia.ContentType != "image/png" || ia.FetchedAt == nil {
		t.Fatalf("unexpected image rows %+v %+v", ia, ib)
	}
	files := coverFiles(t, s)
	if len(files) != 3 {
		t.Fatalf("files = %v, want 3 shared files", files)
	}
	for _, f := range files {
		if !strings.Contains(f, "/covers/"+(*ia.Sha256)[:2]+"/"+*ia.Sha256+"-") {
			t.Errorf("unexpected path %s", f)
		}
	}
}

func TestFetchFailures(t *testing.T) {
	s := testutil.NewServer(t)
	srv := imageServer(t)
	loopback := safehttp.New(safehttp.Options{AllowLoopback: true})
	strict := safehttp.New(safehttp.Options{})
	cases := []struct {
		name   string
		url    string
		client *safehttp.Client
		want   string
	}{
		{"non-image", srv.URL + "/page.html", loopback, "not a supported image"},
		{"oversized", srv.URL + "/huge.png", loopback, "too large"},
		{"loopback", srv.URL + "/a.png", strict, "not allowed"},
		{"metadata", "http://169.254.169.254/latest/meta-data/", strict, "not allowed"},
		{"scheme", "file:///etc/passwd", strict, "http and https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := enqueue(t, s, tc.url)
			if err := work(t, s, tc.client, id, 1); err != nil {
				t.Fatalf("Work returned %v; permanent failures should not retry", err)
			}
			img := getImage(t, s, id)
			if img.Status != db.ImageStatusFailed || img.Error == nil || !strings.Contains(*img.Error, tc.want) {
				t.Fatalf("status %s error %v, want failed containing %q", img.Status, img.Error, tc.want)
			}
		})
	}
	if files := coverFiles(t, s); len(files) != 0 {
		t.Fatalf("failures wrote files: %v", files)
	}
}

func TestFetchRetriesTransientErrors(t *testing.T) {
	s := testutil.NewServer(t)
	srv := imageServer(t)
	client := safehttp.New(safehttp.Options{AllowLoopback: true})
	id := enqueue(t, s, srv.URL+"/broken")
	if err := work(t, s, client, id, 1); err == nil {
		t.Fatal("first attempt should return an error to retry")
	}
	if getImage(t, s, id).Status != db.ImageStatusPending {
		t.Fatal("image should stay pending between retries")
	}
	if err := work(t, s, client, id, 3); err != nil {
		t.Fatalf("last attempt: %v", err)
	}
	if getImage(t, s, id).Status != db.ImageStatusFailed {
		t.Fatal("last attempt should mark the image failed")
	}
}

func TestCoverHandler(t *testing.T) {
	s := testutil.NewServer(t)
	srv := imageServer(t)
	id := enqueue(t, s, srv.URL+"/a.png")
	c := s.Client(t)
	path := images.CoverURL(&id, db.MediaKindGame, images.Card)
	if !strings.HasPrefix(path, "/covers/"+strconv.FormatInt(id, 10)+"/card") {
		t.Fatalf("CoverURL = %s", path)
	}

	res := c.Get(path)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != images.PlaceholderURL(db.MediaKindGame) {
		t.Fatalf("pending: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("pending cache-control %q", cc)
	}

	if err := work(t, s, safehttp.New(safehttp.Options{AllowLoopback: true}), id, 1); err != nil {
		t.Fatal(err)
	}
	for _, size := range []images.Size{images.Thumb, images.Card, images.Full} {
		res = c.Get(images.CoverURL(&id, db.MediaKindGame, size))
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s: %d %s", size, res.StatusCode, res.Header.Get("Content-Type"))
		}
		if res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("ready cache-control %q", res.Header.Get("Cache-Control"))
		}
		cfg, _, err := image.DecodeConfig(strings.NewReader(res.Body))
		if err != nil || cfg.Width != map[images.Size]int{images.Thumb: 200, images.Card: 400, images.Full: 600}[size] {
			t.Fatalf("%s: %+v %v", size, cfg, err)
		}
	}

	if res := c.Get("/covers/" + strconv.FormatInt(id, 10) + "/huge"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("bad size: %d", res.StatusCode)
	}
	if res := c.Get("/covers/999999/thumb?k=film"); res.StatusCode != http.StatusFound || res.Header.Get("Location") != images.PlaceholderURL(db.MediaKindFilm) {
		t.Fatalf("missing image: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if images.CoverURL(nil, db.MediaKindShow, images.Thumb) != images.PlaceholderURL(db.MediaKindShow) {
		t.Fatal("nil image should use the placeholder")
	}
}

func TestUploadCover(t *testing.T) {
	s := testutil.NewServer(t)
	creator := testutil.CreateUser(t, s.Q, "carol")
	other := testutil.CreateUser(t, s.Q, "otto")
	admin := testutil.CreateUser(t, s.Q, "alice", testutil.Admin)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	if _, err := s.Pool.Exec(context.Background(), "update media_items set created_by_user_id = $1 where id = $2", creator.ID, item.ID); err != nil {
		t.Fatal(err)
	}
	path := "/items/" + item.PublicID.String() + "/cover"
	itemPath := "/items/" + item.PublicID.String()
	loadItem := func() db.MediaItem {
		it, err := s.Q.GetMediaItemByPublicID(context.Background(), item.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}

	if res := s.Client(t).Get(path); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous: %d", res.StatusCode)
	}

	cc := s.LoginAs(t, creator)
	res := cc.Get(path)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `href="`+itemPath+`"`) || !strings.Contains(res.Body, `enctype="multipart/form-data"`) {
		t.Fatalf("cover page: %d\n%s", res.StatusCode, res.Body)
	}

	res = cc.Multipart(path, nil, "cover", "cover.jpg", []byte("GIF89a but not really an image"))
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "supported image") {
		t.Fatalf("bad upload: %d", res.StatusCode)
	}
	if loadItem().CoverImageID != nil {
		t.Fatal("bad upload set a cover")
	}

	res = cc.Multipart(path, nil, "cover", "cover.jpg", pngBytes(t, 1000, 1500))
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != itemPath {
		t.Fatalf("upload: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), res.Body)
	}
	first := loadItem().CoverImageID
	if first == nil {
		t.Fatal("cover not set")
	}
	img := getImage(t, s, *first)
	if img.Status != db.ImageStatusReady || img.SourceUrl != nil || *img.Width != 1000 {
		t.Fatalf("uploaded image %+v", img)
	}
	if res := cc.Get(images.CoverURL(first, item.Kind, images.Full)); res.StatusCode != http.StatusOK {
		t.Fatalf("serve uploaded: %d", res.StatusCode)
	}

	oc := s.LoginAs(t, other)
	if res := oc.Get(path); res.StatusCode != http.StatusForbidden {
		t.Fatalf("other GET: %d", res.StatusCode)
	}
	if res := oc.Multipart(path, nil, "cover", "cover.jpg", pngBytes(t, 10, 10)); res.StatusCode != http.StatusForbidden {
		t.Fatalf("other POST: %d", res.StatusCode)
	}
	if *loadItem().CoverImageID != *first {
		t.Fatal("other user changed the cover")
	}

	ac := s.LoginAs(t, admin)
	if res := ac.Multipart(path, nil, "cover", "cover.jpg", pngBytes(t, 50, 75)); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin replace: %d", res.StatusCode)
	}
	if now := loadItem().CoverImageID; now == nil || *now == *first {
		t.Fatal("admin did not replace the cover")
	}

	if res := cc.Get("/items/00000000-0000-0000-0000-000000000000/cover"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing item: %d", res.StatusCode)
	}
}
