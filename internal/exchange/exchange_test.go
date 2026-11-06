package exchange_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/exchange"
	"github.com/casmith/openshelf/internal/testutil"
)

func count(t *testing.T, s *testutil.Server, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exec(t *testing.T, s *testutil.Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// createExperience inserts directly; testutil.CreateExperience has a parameter typing bug.
func createExperience(t *testing.T, s *testutil.Server, userID, itemID int64, state db.ExperienceState, started, finished string) int64 {
	t.Helper()
	var id int64
	err := s.Pool.QueryRow(context.Background(),
		`insert into experiences (user_id, media_item_id, state, started_on, finished_on)
		 values ($1, $2, $3, nullif($4, '')::date, nullif($5, '')::date) returning id`,
		userID, itemID, state, started, finished).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func upload(t *testing.T, c *testutil.Client, filename string, content []byte, preview bool) *testutil.Response {
	t.Helper()
	fields := url.Values{}
	if preview {
		fields.Set("preview", "on")
	}
	return c.Multipart("/settings/import", fields, "file", filename, content)
}

type job struct {
	ID       int64
	PublicID uuid.UUID
}

// importFile uploads content with a preview, runs the dry run, confirms, and runs the import.
func importFile(t *testing.T, s *testutil.Server, c *testutil.Client, filename string, content []byte) (job, *testutil.Response) {
	t.Helper()
	res := upload(t, c, filename, content, true)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: status %d\n%s", res.StatusCode, res.Body)
	}
	j := jobFromLocation(t, s, res.Header.Get("Location"))
	process(t, s, j.ID, true)
	preview := c.Get("/settings/import/" + j.PublicID.String())
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("preview page: status %d", preview.StatusCode)
	}
	res = c.PostForm("/settings/import/"+j.PublicID.String()+"/confirm", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirm: status %d\n%s", res.StatusCode, res.Body)
	}
	process(t, s, j.ID, false)
	return j, preview
}

func jobFromLocation(t *testing.T, s *testutil.Server, loc string) job {
	t.Helper()
	pid, err := uuid.Parse(strings.TrimPrefix(loc, "/settings/import/"))
	if err != nil {
		t.Fatalf("redirect location %q: %v", loc, err)
	}
	j := job{PublicID: pid}
	if err := s.Pool.QueryRow(context.Background(), `select id from import_jobs where public_id = $1`, pid).Scan(&j.ID); err != nil {
		t.Fatal(err)
	}
	return j
}

func process(t *testing.T, s *testutil.Server, id int64, dry bool) {
	t.Helper()
	if err := exchange.Process(context.Background(), s.Deps, exchange.ImportArgs{JobID: id, DryRun: dry}); err != nil {
		t.Fatalf("process job %d (dry=%v): %v", id, dry, err)
	}
}

type jobResult struct {
	Status                    string
	Created, Matched, Skipped int
	Errors                    []exchange.RowError
}

func result(t *testing.T, s *testutil.Server, id int64) jobResult {
	t.Helper()
	var r jobResult
	var errs []byte
	err := s.Pool.QueryRow(context.Background(),
		`select status, created_count, matched_count, skipped_count, row_errors from import_jobs where id = $1`, id).
		Scan(&r.Status, &r.Created, &r.Matched, &r.Skipped, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(errs, &r.Errors); err != nil {
		t.Fatal(err)
	}
	return r
}

func export(t *testing.T, c *testutil.Client) (exchange.Export, []byte) {
	t.Helper()
	res := c.Get("/settings/export.json")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export: status %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment;") {
		t.Errorf("Content-Disposition = %q", res.Header.Get("Content-Disposition"))
	}
	var doc exchange.Export
	if err := json.Unmarshal([]byte(res.Body), &doc); err != nil {
		t.Fatalf("decode export: %v\n%s", err, res.Body)
	}
	return doc, []byte(res.Body)
}

func personalCounts(t *testing.T, s *testutil.Server, userID int64) string {
	t.Helper()
	return fmt.Sprintf("entries=%d experiences=%d progress=%d reviews=%d lists=%d list_items=%d activity=%d items=%d editions=%d",
		count(t, s, `select count(*) from library_entries where user_id = $1`, userID),
		count(t, s, `select count(*) from experiences where user_id = $1`, userID),
		count(t, s, `select count(*) from progress_events pe join experiences x on x.id = pe.experience_id where x.user_id = $1`, userID),
		count(t, s, `select count(*) from reviews where user_id = $1`, userID),
		count(t, s, `select count(*) from lists where user_id = $1`, userID),
		count(t, s, `select count(*) from list_items li join lists l on l.id = li.list_id where l.user_id = $1`, userID),
		count(t, s, `select count(*) from activity_events where user_id = $1`, userID),
		count(t, s, `select count(*) from media_items`),
		count(t, s, `select count(*) from editions`))
}

// seedAlice gives alice one of everything the export covers.
func seedAlice(t *testing.T, s *testutil.Server, alice db.User) {
	t.Helper()
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	exec(t, s, `update media_items set release_year = 1999 where id = $1`, game.ID)
	exec(t, s, `insert into external_ids (provider, value, media_item_id) values ('igdb_game', '1234', $1)`, game.ID)
	dc := testutil.CreateEdition(t, s.Pool, game.ID, "dreamcast")
	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")

	exec(t, s, `insert into library_entries (user_id, media_item_id, edition_id, ownership_user, tags, notes, visibility)
		values ($1, $2, $3, 'owned', '{retro,sega}', 'boxed', 'public')`, alice.ID, game.ID, dc.ID)
	exec(t, s, `insert into library_entries (user_id, media_item_id, backlog) values ($1, $2, true)`, alice.ID, book.ID)
	exp := createExperience(t, s, alice.ID, game.ID, db.ExperienceStateFinished, "2024-01-02", "2024-02-03")
	exec(t, s, `update experiences set edition_id = $2, progress_unit = 'hours', progress_current = 30, progress_total = 30 where id = $1`, exp, dc.ID)
	exec(t, s, `insert into progress_events (experience_id, occurred_at, value, note) values ($1, '2024-01-10T10:00:00Z', 12.5, 'disc 2')`, exp)
	createExperience(t, s, alice.ID, book.ID, db.ExperienceStateActive, "2024-05-01", "")
	exec(t, s, `insert into reviews (user_id, media_item_id, experience_id, rating, body, contains_spoilers)
		values ($1, $2, $3, 90, 'Forklift simulator.', true)`, alice.ID, game.ID, exp)
	l := testutil.CreateList(t, s.Pool, alice.ID, "Favorites", db.VisibilityPrivate, film.ID, game.ID)
	exec(t, s, `update lists set ranked = true, description = 'best of' where id = $1`, l.ID)
	exec(t, s, `insert into activity_events (user_id, verb, media_item_id, experience_id, occurred_at)
		values ($1, 'finished', $2, $3, '2024-02-03T12:00:00Z')`, alice.ID, game.ID, exp)
	exec(t, s, `insert into activity_events (user_id, verb, media_item_id, list_id, occurred_at)
		values ($1, 'listed', $2, $3, '2024-03-01T08:30:00Z')`, alice.ID, film.ID, l.ID)
}

// canonical renders an export without record IDs or timestamps, resolving references by content.
func canonical(doc exchange.Export) []string {
	items := map[uuid.UUID]string{}
	for _, it := range doc.Items {
		year := ""
		if it.Year != nil {
			year = fmt.Sprint(*it.Year)
		}
		items[it.ID] = fmt.Sprintf("%s/%s/%s/%v", it.Kind, it.Title, year, it.ExternalIDs)
	}
	eds := map[uuid.UUID]string{}
	for _, e := range doc.Editions {
		eds[e.ID] = fmt.Sprintf("%s@%s", items[e.Item], deref(e.Platform))
	}
	ed := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return eds[*id]
	}
	exps := map[uuid.UUID]string{}
	var out []string
	for _, e := range doc.LibraryEntries {
		out = append(out, fmt.Sprintf("entry %s %s own=%v backlog=%v tags=%v notes=%s", items[e.Item], ed(e.Edition),
			derefOwn(e.Ownership), e.Backlog, e.Tags, deref(e.Notes)))
	}
	for _, e := range doc.Experiences {
		exps[e.ID] = fmt.Sprintf("%s %s", items[e.Item], e.State)
		pe, _ := json.Marshal(e.ProgressEvents)
		out = append(out, fmt.Sprintf("exp %s %s %s %s-%s %v/%v %s", items[e.Item], ed(e.Edition), e.State, deref(e.StartedOn),
			deref(e.FinishedOn), derefF(e.ProgressCurrent), derefF(e.ProgressTotal), pe))
	}
	for _, r := range doc.Reviews {
		x := ""
		if r.Experience != nil {
			x = exps[*r.Experience]
		}
		out = append(out, fmt.Sprintf("review %s %v %s spoilers=%v exp=%s", items[r.Item], *r.Rating, deref(r.Body), r.ContainsSpoilers, x))
	}
	for _, l := range doc.Lists {
		var li []string
		for _, it := range l.Items {
			li = append(li, fmt.Sprintf("%d:%s", it.Position, items[it.Item]))
		}
		out = append(out, fmt.Sprintf("list %s %s ranked=%v %v", l.Name, deref(l.Description), l.Ranked, li))
	}
	for _, a := range doc.Activity {
		out = append(out, fmt.Sprintf("activity %s %s %s exp=%v list=%v", a.Verb, items[a.Item], a.OccurredAt.UTC().Format(time.RFC3339),
			a.Experience != nil, a.List != nil))
	}
	slices.Sort(out)
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefF(f *float64) string {
	if f == nil {
		return ""
	}
	return fmt.Sprint(*f)
}

func derefOwn(o *db.Ownership) string {
	if o == nil {
		return ""
	}
	return string(*o)
}

func wipe(t *testing.T, s *testutil.Server, userID int64) {
	t.Helper()
	for _, tbl := range []string{"activity_events", "reviews", "experiences", "library_entries", "lists"} {
		exec(t, s, "delete from "+tbl+" where user_id = $1", userID)
	}
}

func TestExportContainsOnlyOwnData(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	seedAlice(t, s, alice)

	secret := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Bob's Secret Book")
	exec(t, s, `insert into library_entries (user_id, media_item_id, notes) values ($1, $2, 'bob notes')`, bob.ID, secret.ID)
	exec(t, s, `insert into reviews (user_id, media_item_id, rating, body, visibility) values ($1, $2, 10, 'bob review', 'public')`, bob.ID, secret.ID)
	testutil.CreateList(t, s.Pool, bob.ID, "Bob list", db.VisibilityPublic, secret.ID)

	doc, raw := export(t, s.LoginAs(t, alice))
	if doc.Format != exchange.FormatV1 || doc.User.Username != "alice" {
		t.Fatalf("header = %q %q", doc.Format, doc.User.Username)
	}
	got := fmt.Sprintf("items=%d editions=%d entries=%d exps=%d reviews=%d lists=%d activity=%d",
		len(doc.Items), len(doc.Editions), len(doc.LibraryEntries), len(doc.Experiences), len(doc.Reviews), len(doc.Lists), len(doc.Activity))
	if want := "items=3 editions=1 entries=2 exps=2 reviews=1 lists=1 activity=2"; got != want {
		t.Errorf("counts: %s, want %s", got, want)
	}
	for _, leak := range []string{"Bob's Secret Book", "bob notes", "bob review", "Bob list", secret.PublicID.String()} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("alice's export contains %q", leak)
		}
	}
	var shenmue exchange.Item
	for _, it := range doc.Items {
		if it.Title == "Shenmue" {
			shenmue = it
		}
	}
	if shenmue.Kind != db.MediaKindGame || shenmue.Year == nil || *shenmue.Year != 1999 ||
		len(shenmue.ExternalIDs) != 1 || shenmue.ExternalIDs[0] != (exchange.ExternalID{Provider: "igdb_game", Value: "1234"}) {
		t.Errorf("embedded item = %+v", shenmue)
	}
	if len(doc.Experiences) > 0 && doc.Experiences[0].ProgressEvents == nil {
		t.Error("progress_events should be an array")
	}

	bobDoc, bobRaw := export(t, s.LoginAs(t, bob))
	if len(bobDoc.Items) != 1 || len(bobDoc.LibraryEntries) != 1 || bytes.Contains(bobRaw, []byte("Shenmue")) || bytes.Contains(bobRaw, []byte("Forklift")) {
		t.Errorf("bob's export leaks or misses data: %s", bobRaw)
	}

	if res := s.Client(t).Get("/settings/export.json"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous export: status %d", res.StatusCode)
	}
	if res := s.LoginAs(t, alice).Get("/settings/export"); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "/settings/export.json") {
		t.Errorf("export page: status %d", res.StatusCode)
	}
}

func TestJSONExportRoundTrips(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	seedAlice(t, s, alice)
	c := s.LoginAs(t, alice)
	before, raw := export(t, c)
	countsBefore := personalCounts(t, s, alice.ID)

	wipe(t, s, alice.ID)
	wiped := personalCounts(t, s, alice.ID)

	res := upload(t, c, "export.json", raw, true)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", res.StatusCode, res.Body)
	}
	j := jobFromLocation(t, s, res.Header.Get("Location"))
	process(t, s, j.ID, true)
	if got := personalCounts(t, s, alice.ID); got != wiped {
		t.Fatalf("dry run wrote data:\n got %s\nwant %s", got, wiped)
	}
	pr := result(t, s, j.ID)
	if pr.Status != exchange.StatusPreviewed || pr.Created != 0 || pr.Matched != 6 || pr.Skipped != 0 || len(pr.Errors) != 0 {
		t.Errorf("preview = %+v", pr)
	}
	page := c.Get("/settings/import/" + j.PublicID.String())
	if !strings.Contains(page.Body, "Import now") || !strings.Contains(page.Body, "dry run") {
		t.Errorf("preview page missing confirm:\n%s", page.Body)
	}
	if res := c.PostForm("/settings/import/"+j.PublicID.String()+"/confirm", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirm: %d", res.StatusCode)
	}
	if n := count(t, s, `select count(*) from river_job where kind = 'exchange_import' and args->>'job_id' = $1`, fmt.Sprint(j.ID)); n != 2 {
		t.Errorf("river jobs for import = %d, want 2 (preview + import)", n)
	}
	process(t, s, j.ID, false)
	if r := result(t, s, j.ID); r.Status != exchange.StatusDone || r.Matched != 6 || len(r.Errors) != 0 {
		t.Errorf("result = %+v", r)
	}

	if got := personalCounts(t, s, alice.ID); got != countsBefore {
		t.Errorf("counts after import:\n got %s\nwant %s", got, countsBefore)
	}
	after, _ := export(t, c)
	if a, b := canonical(after), canonical(before); !slices.Equal(a, b) {
		t.Errorf("round trip differs:\n got %s\nwant %s", strings.Join(a, "\n     "), strings.Join(b, "\n     "))
	}
	if n := count(t, s, `select count(*) from library_entries where user_id = $1 and visibility <> 'private'`, alice.ID); n != 0 {
		t.Errorf("%d imported entries are not private", n)
	}

	j2, _ := importFile(t, s, c, "export.json", raw)
	if r := result(t, s, j2.ID); r.Created != 0 || r.Matched != 0 || r.Skipped != 6 {
		t.Errorf("re-import result = %+v, want all skipped", r)
	}
	if got := personalCounts(t, s, alice.ID); got != countsBefore {
		t.Errorf("re-import changed counts:\n got %s\nwant %s", got, countsBefore)
	}
}

func TestJSONImportIntoFreshInstanceCreatesItems(t *testing.T) {
	src := testutil.NewServer(t)
	alice := testutil.CreateUser(t, src.Q, "alice")
	seedAlice(t, src, alice)
	_, raw := export(t, src.LoginAs(t, alice))

	dst := testutil.NewServer(t)
	carol := testutil.CreateUser(t, dst.Q, "carol")
	j, _ := importFile(t, dst, dst.LoginAs(t, carol), "export.json", raw)
	if r := result(t, dst, j.ID); r.Status != exchange.StatusDone || r.Created != 3 || len(r.Errors) != 0 {
		t.Fatalf("result = %+v", r)
	}
	if n := count(t, dst, `select count(*) from media_items where created_by_user_id = $1`, carol.ID); n != 3 {
		t.Errorf("manual items created by carol = %d, want 3", n)
	}
	if n := count(t, dst, `select count(*) from external_ids x join media_items m on m.id = x.media_item_id
		where x.provider = 'igdb_game' and x.value = '1234' and m.title = 'Shenmue'`); n != 1 {
		t.Error("external ID not recorded on created item")
	}
	if n := count(t, dst, `select count(*) from editions e join platforms p on p.id = e.platform_id where p.slug = 'dreamcast'`); n != 1 {
		t.Error("dreamcast edition not recreated")
	}
}

func TestJSONImportKeepsUndatedFinishes(t *testing.T) {
	src := testutil.NewServer(t)
	alice := testutil.CreateUser(t, src.Q, "alice")
	game := testutil.CreateItem(t, src.Pool, db.MediaKindGame, "Shenmue")
	createExperience(t, src, alice.ID, game.ID, db.ExperienceStateFinished, "", "")
	doc, raw := export(t, src.LoginAs(t, alice))
	if len(doc.Experiences) != 1 || doc.Experiences[0].FinishedOn != nil {
		t.Fatalf("exported experiences = %+v", doc.Experiences)
	}
	blank := strings.Replace(string(raw), `"state":"finished"`, `"state":"finished","finished_on":""`, 1)
	if blank == string(raw) {
		t.Fatal("couldn't add a blank finished_on to the export")
	}

	for name, content := range map[string]string{"omitted": string(raw), "blank": blank} {
		dst := testutil.NewServer(t)
		carol := testutil.CreateUser(t, dst.Q, "carol")
		j, _ := importFile(t, dst, dst.LoginAs(t, carol), "export.json", []byte(content))
		if r := result(t, dst, j.ID); r.Status != exchange.StatusDone || len(r.Errors) != 0 {
			t.Fatalf("%s: result = %+v", name, r)
		}
		if n := count(t, dst, `select count(*) from experiences where user_id = $1 and state = 'finished' and finished_on is null`, carol.ID); n != 1 {
			t.Errorf("%s: undated finish not kept as null", name)
		}
		if n := count(t, dst, `select count(*) from activity_events where user_id = $1 and verb = 'finished'`, carol.ID); n != 0 {
			t.Errorf("%s: undated finish recorded %d finished events", name, n)
		}
	}
}

const csvHeader = "kind,title,year,isbn13,tmdb_id,igdb_id,platform,ownership,backlog,status,started_on,finished_on,rating_100,review,tags\n"

func TestCSVImportBlankFinishDate(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	csv := csvHeader +
		"game,Beaten Long Ago,1998,,,,,owned,,finished,,,,,\n" +
		"game,Dropped Long Ago,1999,,,,,owned,,abandoned,,,,,\n" +
		"game,Dated Finish,2000,,,,,owned,,finished,,2021-06-01,,,\n"
	j, _ := importFile(t, s, c, "old.csv", []byte(csv))
	if r := result(t, s, j.ID); r.Status != exchange.StatusDone || len(r.Errors) != 0 {
		t.Fatalf("result = %+v", r)
	}
	if n := count(t, s, `select count(*) from experiences where user_id = $1 and state in ('finished', 'abandoned') and finished_on is null`, alice.ID); n != 2 {
		t.Errorf("undated finishes stored with a date: %d null, want 2", n)
	}
	if n := count(t, s, `select count(*) from activity_events where user_id = $1 and verb in ('finished', 'abandoned')`, alice.ID); n != 1 {
		t.Errorf("%d finish events, want 1 for the dated row only", n)
	}
	if n := count(t, s, `select count(*) from activity_events where user_id = $1 and verb = 'finished' and occurred_at::date = '2021-06-01'`, alice.ID); n != 1 {
		t.Error("dated finish not recorded at its date")
	}
	j2, _ := importFile(t, s, c, "old.csv", []byte(csv))
	if r := result(t, s, j2.ID); r.Skipped != 3 {
		t.Errorf("re-import = %+v, want 3 skipped", r)
	}
}

func TestCSVImportMatchesAndIsIdempotent(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)

	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	ed := testutil.CreateEdition(t, s.Pool, book.ID, "")
	exec(t, s, `insert into external_ids (provider, value, edition_id) values ('isbn13', '9780441013593', $1)`, ed.ID)
	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")
	exec(t, s, `insert into external_ids (provider, value, media_item_id) values ('tmdb_movie', '329865', $1)`, film.ID)
	show := testutil.CreateItem(t, s.Pool, db.MediaKindShow, "Arrival")
	exec(t, s, `insert into external_ids (provider, value, media_item_id) values ('tmdb_tv', '329865', $1)`, show.ID)
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	exec(t, s, `insert into external_ids (provider, value, media_item_id) values ('igdb_game', '1234', $1)`, game.ID)
	titled := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Jet Set Radio")
	exec(t, s, `update media_items set release_year = 2000 where id = $1`, titled.ID)
	itemsBefore := count(t, s, `select count(*) from media_items`)

	csv := csvHeader +
		"book,Dune (renamed),,978-0-441-01359-3,,,,owned,,finished,2023-01-01,2023-02-01,80,Spice.,scifi;classic\n" +
		"film,Some Other Title,,,329865,,,,true,,,,,,\n" +
		"game,Shenmue II,,,,1234,Dreamcast,owned,,in_progress,2024-06-01,,,,\n" +
		"game,jet set radio,2000,,,,,borrowed,,,,,95,,\n" +
		"game,Brand New Game,2021,,,999,,not_owned,true,abandoned,,2022-03-04,,,\n"
	j, preview := importFile(t, s, c, "mine.csv", []byte(csv))
	if !strings.Contains(preview.Body, "Ready to import") {
		t.Errorf("preview page:\n%s", preview.Body)
	}
	r := result(t, s, j.ID)
	if r.Status != exchange.StatusDone || r.Created != 1 || r.Matched != 4 || r.Skipped != 0 || len(r.Errors) != 0 {
		t.Fatalf("result = %+v", r)
	}
	if n := count(t, s, `select count(*) from media_items`); n != itemsBefore+1 {
		t.Errorf("items = %d, want %d", n, itemsBefore+1)
	}
	for _, it := range []db.MediaItem{book, film, game, titled} {
		if n := count(t, s, `select count(*) from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, it.ID); n != 1 {
			t.Errorf("%s %s: %d entries, want 1", it.Kind, it.Title, n)
		}
	}
	if n := count(t, s, `select count(*) from library_entries where media_item_id = $1`, show.ID); n != 0 {
		t.Error("film tmdb_id matched the show")
	}
	var created db.MediaItem
	if err := s.Pool.QueryRow(context.Background(), `select id, created_by_user_id, release_year, sort_title from media_items where title = 'Brand New Game'`).
		Scan(&created.ID, &created.CreatedByUserID, &created.ReleaseYear, &created.SortTitle); err != nil {
		t.Fatal(err)
	}
	if created.CreatedByUserID == nil || *created.CreatedByUserID != alice.ID || *created.ReleaseYear != 2021 {
		t.Errorf("created item = %+v", created)
	}
	if n := count(t, s, `select count(*) from external_ids where provider = 'igdb_game' and value = '999' and media_item_id = $1`, created.ID); n != 1 {
		t.Error("igdb_id not recorded on created item")
	}
	if n := count(t, s, `select count(*) from library_entries le join editions e on e.id = le.edition_id join platforms p on p.id = e.platform_id
		where le.user_id = $1 and le.media_item_id = $2 and p.slug = 'dreamcast'`, alice.ID, game.ID); n != 1 {
		t.Error("platform not mapped to a Dreamcast edition")
	}
	if n := count(t, s, `select count(*) from library_entries where user_id = $1 and media_item_id = $2
		and ownership_user = 'owned' and tags = '{scifi,classic}' and visibility = 'private'`, alice.ID, book.ID); n != 1 {
		t.Error("book entry fields not imported")
	}
	if n := count(t, s, `select count(*) from library_entries where user_id = $1 and media_item_id = $2 and backlog and ownership_user is null`, alice.ID, film.ID); n != 1 {
		t.Error("film backlog entry not imported")
	}
	if n := count(t, s, `select count(*) from experiences where user_id = $1 and media_item_id = $2 and state = 'finished'
		and started_on = '2023-01-01' and finished_on = '2023-02-01'`, alice.ID, book.ID); n != 1 {
		t.Error("finished experience not imported")
	}
	if n := count(t, s, `select count(*) from experiences where user_id = $1 and media_item_id = $2 and state = 'abandoned'`, alice.ID, created.ID); n != 1 {
		t.Error("abandoned experience not imported")
	}
	if n := count(t, s, `select count(*) from reviews r join experiences x on x.id = r.experience_id
		where r.user_id = $1 and r.media_item_id = $2 and r.rating = 80 and r.body = 'Spice.' and r.visibility = 'private'`, alice.ID, book.ID); n != 1 {
		t.Error("review not imported or not linked to its experience")
	}
	if n := count(t, s, `select count(*) from activity_events where user_id = $1 and media_item_id = $2
		and verb = 'finished' and occurred_at::date = '2023-02-01' and visibility = 'private'`, alice.ID, book.ID); n != 1 {
		t.Error("finished activity not recorded at the historical date")
	}
	if n := count(t, s, `select count(*) from activity_events where user_id = $1 and media_item_id = $2
		and verb = 'started' and occurred_at::date = '2023-01-01'`, alice.ID, book.ID); n != 1 {
		t.Error("started activity not recorded at the historical date")
	}

	countsBefore := personalCounts(t, s, alice.ID)
	j2, _ := importFile(t, s, c, "mine.csv", []byte(csv))
	if r := result(t, s, j2.ID); r.Created != 0 || r.Matched != 0 || r.Skipped != 5 || len(r.Errors) != 0 {
		t.Errorf("second import = %+v, want 5 skipped", r)
	}
	if got := personalCounts(t, s, alice.ID); got != countsBefore {
		t.Errorf("second import changed data:\n got %s\nwant %s", got, countsBefore)
	}
}

func TestCSVBadRowsAreReportedWithLineNumbers(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	csv := csvHeader +
		"game,Good One,,,,,,owned,,,,,,,\n" +
		"toaster,Bad Kind,,,,,,,,,,,,,\n" +
		"book,Bad Date,,,,,,,,finished,,2023-13-45,,,\n" +
		"film,Bad Rating,,,,,,,,,,,101,,\n" +
		"game,Bad Platform,,,,,Toaster 9000,,,,,,,,\n" +
		"book,\"Multi\nLine\",,,,,,,,,,,,,\n" +
		",,,,,,,,,,,,,,\n" +
		"show,Good Two,,,,,,,,in_progress,2024-01-01,,,,\n"
	j, preview := importFile(t, s, c, "bad.csv", []byte(csv))
	if !strings.Contains(preview.Body, "Line 3") {
		t.Errorf("preview doesn't list bad rows:\n%s", preview.Body)
	}
	r := result(t, s, j.ID)
	if r.Status != exchange.StatusDone {
		t.Fatalf("status = %s", r.Status)
	}
	var lines []int
	for _, e := range r.Errors {
		lines = append(lines, e.Line)
	}
	if want := []int{3, 4, 5, 6}; !slices.Equal(lines, want) {
		t.Errorf("error lines = %v, want %v (%+v)", lines, want, r.Errors)
	}
	if r.Created != 3 {
		t.Errorf("created = %d, want 3 (Good One, Multi Line, Good Two)", r.Created)
	}
	for _, title := range []string{"Good One", "Good Two", "Multi\nLine"} {
		if n := count(t, s, `select count(*) from library_entries le join media_items m on m.id = le.media_item_id where le.user_id = $1 and m.title = $2`, alice.ID, title); n != 1 {
			t.Errorf("%q not imported", title)
		}
	}
	if n := count(t, s, `select count(*) from media_items where title = 'Bad Platform'`); n != 0 {
		t.Error("failed row left a catalog item behind")
	}
}

func TestImportFileErrors(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)

	if res := upload(t, c, "empty.csv", []byte("  \n"), true); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "The file is empty.") {
		t.Errorf("empty file: status %d", res.StatusCode)
	}
	big := bytes.Repeat([]byte("a"), exchange.MaxUploadBytes+10)
	if res := upload(t, c, "big.csv", big, true); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "10 MB") {
		t.Errorf("oversized file: status %d", res.StatusCode)
	}

	res := upload(t, c, "other.json", []byte(`{"format":"something.else"}`), true)
	j := jobFromLocation(t, s, res.Header.Get("Location"))
	process(t, s, j.ID, true)
	if r := result(t, s, j.ID); r.Status != exchange.StatusFailed {
		t.Errorf("unsupported format status = %s", r.Status)
	}
	if page := c.Get("/settings/import/" + j.PublicID.String()); !strings.Contains(page.Body, "Unsupported export format") {
		t.Errorf("failed job page:\n%s", page.Body)
	}
	if res := c.PostForm("/settings/import/"+j.PublicID.String()+"/confirm", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("confirming a failed job: status %d", res.StatusCode)
	}

	res = upload(t, c, "noheader.csv", []byte("game,Shenmue\n"), false)
	j = jobFromLocation(t, s, res.Header.Get("Location"))
	process(t, s, j.ID, false)
	if r := result(t, s, j.ID); r.Status != exchange.StatusFailed {
		t.Errorf("missing header status = %s", r.Status)
	}
}

func TestImportWithoutPreviewQueuesDirectly(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	res := upload(t, c, "one.csv", []byte(csvHeader+"film,Heat,1995,,,,,owned,,,,,,,\n"), false)
	j := jobFromLocation(t, s, res.Header.Get("Location"))
	if r := result(t, s, j.ID); r.Status != exchange.StatusQueued {
		t.Fatalf("status = %s", r.Status)
	}
	if n := count(t, s, `select count(*) from river_job where kind = 'exchange_import' and (args->>'dry_run')::boolean = false`); n != 1 {
		t.Errorf("river jobs = %d", n)
	}
	process(t, s, j.ID, true)
	if r := result(t, s, j.ID); r.Status != exchange.StatusQueued {
		t.Errorf("dry-run worker touched a queued job: %s", r.Status)
	}
	process(t, s, j.ID, false)
	process(t, s, j.ID, false)
	if n := count(t, s, `select count(*) from library_entries where user_id = $1`, alice.ID); n != 1 {
		t.Errorf("entries = %d", n)
	}
	if partial := c.Form(http.MethodGet, "/settings/import/"+j.PublicID.String(), nil, true); strings.Contains(partial.Body, "<html") || !strings.Contains(partial.Body, "import-status") {
		t.Errorf("partial status should be a fragment:\n%s", partial.Body)
	}
}

func TestImportJobsAreIsolatedBetweenUsers(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	ac, bc := s.LoginAs(t, alice), s.LoginAs(t, bob)

	res := upload(t, ac, "alice-private.csv", []byte(csvHeader+"book,Alice Diary,,,,,,owned,,,,,,,\n"), true)
	j := jobFromLocation(t, s, res.Header.Get("Location"))
	process(t, s, j.ID, true)

	path := "/settings/import/" + j.PublicID.String()
	if res := bc.Get(path); res.StatusCode != http.StatusNotFound {
		t.Errorf("bob viewing alice's import: status %d", res.StatusCode)
	}
	if res := bc.PostForm(path+"/confirm", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("bob confirming alice's import: status %d", res.StatusCode)
	}
	if r := result(t, s, j.ID); r.Status != exchange.StatusPreviewed {
		t.Errorf("status after bob's confirm = %s", r.Status)
	}
	if page := bc.Get("/settings/import"); strings.Contains(page.Body, "alice-private.csv") {
		t.Error("bob's import page lists alice's job")
	}
	if page := ac.Get("/settings/import"); !strings.Contains(page.Body, "alice-private.csv") {
		t.Error("alice's import page doesn't list her job")
	}

	ac.PostForm(path+"/confirm", nil)
	process(t, s, j.ID, false)
	if n := count(t, s, `select count(*) from library_entries where user_id = $1`, bob.ID); n != 0 {
		t.Errorf("alice's import wrote %d entries for bob", n)
	}
	if n := count(t, s, `select count(*) from library_entries where user_id = $1`, alice.ID); n != 1 {
		t.Errorf("alice entries = %d", n)
	}
	if _, raw := export(t, bc); bytes.Contains(raw, []byte("Alice Diary")) {
		t.Error("bob's export contains alice's imported item")
	}
}
