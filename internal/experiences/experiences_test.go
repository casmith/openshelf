package experiences_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/experiences"
	"github.com/casmith/openshelf/internal/testutil"
)

var ctx = context.Background()

func exec(t *testing.T, s *testutil.Server, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// createExperience stands in for testutil.CreateExperience, whose SQL fails to
// type its state parameter.
func createExperience(t *testing.T, s *testutil.Server, userID, itemID int64, state db.ExperienceState) db.Experience {
	t.Helper()
	e, err := s.Q.ExperiencesInsert(ctx, db.ExperiencesInsertParams{
		UserID: userID, MediaItemID: itemID, State: state, Visibility: db.VisibilityPrivate,
		StartedOn: ptr(time.Now().UTC().Truncate(24 * time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func ptr[T any](v T) *T { return &v }

var ny, _ = time.LoadLocation("America/New_York")

func eventTime(t *testing.T, s *testutil.Server, e db.Experience, verb string) time.Time {
	t.Helper()
	var at time.Time
	if err := s.Pool.QueryRow(ctx, `select occurred_at from activity_events where experience_id = $1 and verb = $2::activity_verb`, e.ID, verb).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func experiencesOf(t *testing.T, s *testutil.Server, u db.User, item db.MediaItem) []db.Experience {
	t.Helper()
	es, err := s.Q.ExperiencesListForItem(ctx, db.ExperiencesListForItemParams{UserID: u.ID, MediaItemID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func verbs(t *testing.T, s *testutil.Server, u db.User) []string {
	t.Helper()
	rows, err := s.Pool.Query(ctx, `select verb::text from activity_events where user_id = $1 order by id`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func expURL(e db.Experience, suffix string) string {
	return "/experiences/" + e.PublicID.String() + suffix
}

func itemURL(item db.MediaItem) string { return "/items/" + item.PublicID.String() }

func wantStatus(t *testing.T, res *testutil.Response, code int) {
	t.Helper()
	if res.StatusCode != code {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, code, res.Body)
	}
}

func TestCreateCopiesDefaultVisibilityAndRecordsActivity(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s, `update users set default_visibility = 'instance' where id = $1`, u.ID)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	c := s.LoginAs(t, u)

	res := c.PostForm(itemURL(item)+"/experiences", nil)
	wantStatus(t, res, http.StatusSeeOther)
	if loc := res.Header.Get("Location"); loc != itemURL(item) {
		t.Fatalf("redirect to %q", loc)
	}
	es := experiencesOf(t, s, u, item)
	if len(es) != 1 {
		t.Fatalf("got %d experiences", len(es))
	}
	e := es[0]
	if e.State != db.ExperienceStateActive || e.StartedOn == nil || e.Visibility != db.VisibilityInstance {
		t.Fatalf("unexpected experience %+v", e)
	}
	if e.ProgressUnit == nil || *e.ProgressUnit != db.ProgressUnitHours || e.ProgressTotal != nil {
		t.Fatalf("game unit: %+v", e)
	}
	var vis string
	if err := s.Pool.QueryRow(ctx, `select visibility::text from activity_events where experience_id = $1 and verb = 'started'`, e.ID).Scan(&vis); err != nil || vis != "instance" {
		t.Fatalf("started activity: %v %q", err, vis)
	}
}

func TestDefaultUnitsByKind(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, u)

	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Anathem")
	ed := testutil.CreateEdition(t, s.Pool, book.ID, "")
	exec(t, s, `update editions set page_count = 937 where id = $1`, ed.ID)
	show := testutil.CreateItem(t, s.Pool, db.MediaKindShow, "The Expanse")
	exec(t, s, `insert into show_details (media_item_id, episode_count) values ($1, 62)`, show.ID)
	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Heat")

	for _, it := range []db.MediaItem{book, show, film} {
		wantStatus(t, c.PostForm(itemURL(it)+"/experiences", nil), http.StatusSeeOther)
	}
	b := experiencesOf(t, s, u, book)[0]
	if *b.ProgressUnit != db.ProgressUnitPages || *b.ProgressTotal != 937 || *b.ProgressCurrent != 0 || b.EditionID == nil {
		t.Fatalf("book: %+v", b)
	}
	sh := experiencesOf(t, s, u, show)[0]
	if *sh.ProgressUnit != db.ProgressUnitEpisodes || *sh.ProgressTotal != 62 {
		t.Fatalf("show: %+v", sh)
	}
	f := experiencesOf(t, s, u, film)[0]
	if f.ProgressUnit != nil || f.ProgressTotal != nil {
		t.Fatalf("film: %+v", f)
	}
}

func TestTransitions(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Jet Set Radio")
	e := createExperience(t, s, u.ID, item.ID, db.ExperienceStateActive)
	c := s.LoginAs(t, u)

	for _, step := range []struct {
		action string
		state  db.ExperienceState
	}{
		{"pause", db.ExperienceStatePaused},
		{"resume", db.ExperienceStateActive},
		{"finish", db.ExperienceStateFinished},
	} {
		wantStatus(t, c.PostForm(expURL(e, "/"+step.action), nil), http.StatusSeeOther)
		if got := experiencesOf(t, s, u, item)[0].State; got != step.state {
			t.Fatalf("after %s: state %s", step.action, got)
		}
	}
	if got := strings.Join(verbs(t, s, u), ","); got != "paused,resumed,finished" {
		t.Fatalf("activity verbs %q", got)
	}

	for _, a := range []string{"pause", "resume", "abandon", "finish", "start"} {
		wantStatus(t, c.PostForm(expURL(e, "/"+a), nil), http.StatusUnprocessableEntity)
		wantStatus(t, c.Form(http.MethodPost, expURL(e, "/"+a), nil, true), http.StatusUnprocessableEntity)
	}
	if got := experiencesOf(t, s, u, item)[0].State; got != db.ExperienceStateFinished {
		t.Fatalf("invalid transition changed state to %s", got)
	}
	if n := len(verbs(t, s, u)); n != 3 {
		t.Fatalf("invalid transitions recorded activity: %d events", n)
	}
}

func TestAbandonSetsFinishedOn(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Infinite Jest")
	e := createExperience(t, s, u.ID, item.ID, db.ExperienceStateActive)
	c := s.LoginAs(t, u)

	wantStatus(t, c.PostForm(expURL(e, "/pause"), nil), http.StatusSeeOther)
	wantStatus(t, c.PostForm(expURL(e, "/abandon"), nil), http.StatusSeeOther)
	got := experiencesOf(t, s, u, item)[0]
	if got.State != db.ExperienceStateAbandoned || got.FinishedOn == nil {
		t.Fatalf("abandon: %+v", got)
	}
	if v := verbs(t, s, u); v[len(v)-1] != "abandoned" {
		t.Fatalf("verbs %v", v)
	}
}

func TestProgressClampsAndAppendsEvents(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	exec(t, s, `update editions set page_count = 412 where id = $1`, testutil.CreateEdition(t, s.Pool, item.ID, "").ID)
	c := s.LoginAs(t, u)
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]

	wantStatus(t, c.PostForm(expURL(e, "/progress"), url.Values{"value": {"120"}}), http.StatusSeeOther)
	res := c.Form(http.MethodPost, expURL(e, "/progress"), url.Values{"value": {"9000"}}, true)
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(res.Body, "<html") || !strings.Contains(res.Body, `id="experiences-panel"`) || !strings.Contains(res.Body, "412 / 412 pages") {
		t.Fatalf("htmx progress should return the panel fragment:\n%s", res.Body)
	}
	got := experiencesOf(t, s, u, item)[0]
	if *got.ProgressCurrent != 412 {
		t.Fatalf("progress past total not clamped: %v", *got.ProgressCurrent)
	}
	var values []float64
	rows, _ := s.Pool.Query(ctx, `select value::float8 from progress_events where experience_id = $1 order by id`, e.ID)
	for rows.Next() {
		var v float64
		_ = rows.Scan(&v)
		values = append(values, v)
	}
	rows.Close()
	if len(values) != 2 || values[0] != 120 || values[1] != 412 {
		t.Fatalf("progress events %v", values)
	}
	if v := verbs(t, s, u); strings.Join(v, ",") != "started,progressed,progressed" {
		t.Fatalf("verbs %v", v)
	}

	wantStatus(t, c.PostForm(expURL(e, "/progress"), url.Values{"value": {"-3"}}), http.StatusUnprocessableEntity)
	wantStatus(t, c.PostForm(expURL(e, "/progress"), url.Values{"value": {"abc"}}), http.StatusUnprocessableEntity)

	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Alien")
	fe := createExperience(t, s, u.ID, film.ID, db.ExperienceStateActive)
	wantStatus(t, c.PostForm(expURL(fe, "/progress"), url.Values{"value": {"1"}}), http.StatusUnprocessableEntity)
}

func TestFilmWatchedTwiceAndRevisit(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Heat")
	c := s.LoginAs(t, u)

	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]
	wantStatus(t, c.PostForm(expURL(e, "/finish"), nil), http.StatusSeeOther)

	page := c.Get(itemURL(item))
	wantStatus(t, page, http.StatusOK)
	if !strings.Contains(page.Body, "Watched once") || !strings.Contains(page.Body, "Revisit") {
		t.Fatalf("after one watch:\n%s", page.Body)
	}

	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	es := experiencesOf(t, s, u, item)
	if len(es) != 2 {
		t.Fatalf("revisit should create a new experience, got %d", len(es))
	}
	second := es[0]
	if second.ID == e.ID || second.State != db.ExperienceStateActive {
		t.Fatalf("revisit: %+v", second)
	}
	wantStatus(t, c.PostForm(expURL(second, "/finish"), nil), http.StatusSeeOther)

	page = c.Get(itemURL(item))
	if !strings.Contains(page.Body, "Watched twice") {
		t.Fatalf("want 'Watched twice':\n%s", page.Body)
	}
}

func TestFinishedWithoutStartAndBackdating(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s, `update users set timezone = 'America/New_York' where id = $1`, u.ID)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Ran")
	c := s.LoginAs(t, u)

	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", url.Values{"mode": {"finished"}, "finished_on": {"2019-05-04"}}), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]
	if e.State != db.ExperienceStateFinished || e.StartedOn != nil || e.FinishedOn.Format(time.DateOnly) != "2019-05-04" {
		t.Fatalf("finished without start: %+v", e)
	}
	if v := verbs(t, s, u); strings.Join(v, ",") != "finished" {
		t.Fatalf("verbs %v", v)
	}
	if at := eventTime(t, s, e, "finished"); !at.Equal(time.Date(2019, 5, 4, 0, 0, 0, 0, ny)) {
		t.Fatalf("backdated finish recorded at %s, want the start of 2019-05-04 in the user's timezone", at)
	}

	wantStatus(t, c.Get(expURL(e, "/edit")), http.StatusOK)
	res := c.PostForm(expURL(e, "/edit"), url.Values{
		"started_on": {"2019-05-03"}, "finished_on": {"2019-05-04"}, "visibility": {"public"}, "notes": {"Rewatch soon"},
	})
	wantStatus(t, res, http.StatusSeeOther)
	e = experiencesOf(t, s, u, item)[0]
	if e.StartedOn.Format(time.DateOnly) != "2019-05-03" || e.Visibility != db.VisibilityPublic || *e.Notes != "Rewatch soon" {
		t.Fatalf("edit: %+v", e)
	}

	for _, bad := range []url.Values{
		{"started_on": {"2019-05-05"}, "finished_on": {"2019-05-04"}, "visibility": {"private"}},
		{"finished_on": {"2999-01-01"}, "visibility": {"private"}},
		{"finished_on": {"2019-05-04"}, "visibility": {"everyone"}},
		{"finished_on": {"2019-05-04"}, "visibility": {"private"}, "progress_unit": {"furlongs"}},
	} {
		wantStatus(t, c.PostForm(expURL(e, "/edit"), bad), http.StatusUnprocessableEntity)
	}
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", url.Values{"mode": {"finished"}, "finished_on": {"2999-01-01"}}), http.StatusUnprocessableEntity)
}

func TestEditUnitClampsCurrent(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Outer Wilds")
	c := s.LoginAs(t, u)
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]
	wantStatus(t, c.PostForm(expURL(e, "/progress"), url.Values{"value": {"30"}}), http.StatusSeeOther)
	wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{
		"started_on": {e.StartedOn.Format(time.DateOnly)}, "visibility": {"private"},
		"progress_unit": {"hours"}, "progress_total": {"20"},
	}), http.StatusSeeOther)
	e = experiencesOf(t, s, u, item)[0]
	if *e.ProgressTotal != 20 || *e.ProgressCurrent != 20 {
		t.Fatalf("edit total: %+v", e)
	}
	wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"visibility": {"private"}, "progress_unit": {"percent"}}), http.StatusSeeOther)
	e = experiencesOf(t, s, u, item)[0]
	if *e.ProgressUnit != db.ProgressUnitPercent || *e.ProgressTotal != 100 || *e.ProgressCurrent != 0 || e.StartedOn != nil {
		t.Fatalf("switch to percent: %+v", e)
	}
}

func TestDelete(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Heat")
	c := s.LoginAs(t, u)
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]

	res := c.Form(http.MethodDelete, expURL(e, ""), nil, true)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "Nothing logged yet") {
		t.Fatalf("delete fragment:\n%s", res.Body)
	}
	if len(experiencesOf(t, s, u, item)) != 0 || len(verbs(t, s, u)) != 0 {
		t.Fatal("experience or its activity survived delete")
	}
	wantStatus(t, c.Form(http.MethodDelete, expURL(e, ""), nil, true), http.StatusNotFound)
}

func TestQuickLog(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindShow, "Severance")
	exec(t, s, `insert into show_details (media_item_id, episode_count) values ($1, 19)`, item.ID)
	render := func() string {
		comp, err := experiences.QuickLog(ctx, s.Deps, u, item)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := comp.Render(ctx, &buf); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if out := render(); out != "" {
		t.Fatalf("no active experience should render nothing, got %q", out)
	}

	c := s.LoginAs(t, u)
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]
	out := render()
	if !strings.Contains(out, `id="quicklog-`+item.PublicID.String()+`"`) || !strings.Contains(out, `name="value"`) || !strings.Contains(out, "Finish") {
		t.Fatalf("quick log:\n%s", out)
	}

	res := c.Form(http.MethodPost, expURL(e, "/progress"), url.Values{"value": {"3"}, "view": {"quick"}}, true)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, `id="quicklog-`) || strings.Contains(res.Body, "experiences-panel") || !strings.Contains(res.Body, "3 / 19 episodes") {
		t.Fatalf("quick progress fragment:\n%s", res.Body)
	}
	res = c.Form(http.MethodPost, expURL(e, "/finish"), url.Values{"view": {"quick"}}, true)
	wantStatus(t, res, http.StatusOK)
	if strings.TrimSpace(res.Body) != "" {
		t.Fatalf("finished quick log should swap to nothing, got %q", res.Body)
	}
}

func TestCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Piranesi")
	exec(t, s, `update editions set page_count = 272 where id = $1`, testutil.CreateEdition(t, s.Pool, item.ID, "").ID)
	ca := s.LoginAs(t, alice)
	wantStatus(t, ca.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	e := experiencesOf(t, s, alice, item)[0]
	exec(t, s, `update experiences set visibility = 'public', notes = 'alice secret note' where id = $1`, e.ID)
	before := experiencesOf(t, s, alice, item)[0]

	cb := s.LoginAs(t, bob)
	for _, a := range []string{"start", "pause", "resume", "finish", "abandon"} {
		wantStatus(t, cb.PostForm(expURL(e, "/"+a), nil), http.StatusNotFound)
	}
	wantStatus(t, cb.PostForm(expURL(e, "/progress"), url.Values{"value": {"10"}}), http.StatusNotFound)
	wantStatus(t, cb.Form(http.MethodPost, expURL(e, "/progress"), url.Values{"value": {"10"}, "view": {"quick"}}, true), http.StatusNotFound)
	wantStatus(t, cb.Get(expURL(e, "/edit")), http.StatusNotFound)
	wantStatus(t, cb.PostForm(expURL(e, "/edit"), url.Values{"visibility": {"private"}}), http.StatusNotFound)
	wantStatus(t, cb.Form(http.MethodDelete, expURL(e, ""), nil, true), http.StatusNotFound)

	page := cb.Get(itemURL(item))
	wantStatus(t, page, http.StatusOK)
	if strings.Contains(page.Body, e.PublicID.String()) || strings.Contains(page.Body, "alice secret note") {
		t.Fatal("bob's item page shows alice's experience")
	}
	if comp, err := experiences.QuickLog(ctx, s.Deps, bob, item); err != nil {
		t.Fatal(err)
	} else {
		var buf bytes.Buffer
		_ = comp.Render(ctx, &buf)
		if buf.Len() != 0 {
			t.Fatal("bob's quick log shows alice's experience")
		}
	}

	after := experiencesOf(t, s, alice, item)
	if len(after) != 1 || after[0].UpdatedAt != before.UpdatedAt || after[0].State != before.State || *after[0].ProgressCurrent != 0 {
		t.Fatalf("bob modified alice's experience: %+v", after)
	}
	if len(verbs(t, s, bob)) != 0 {
		t.Fatal("bob's requests recorded activity")
	}

	// Bob's own experience of the same item is independent of alice's.
	wantStatus(t, cb.PostForm(itemURL(item)+"/experiences", nil), http.StatusSeeOther)
	if len(experiencesOf(t, s, bob, item)) != 1 || len(experiencesOf(t, s, alice, item)) != 1 {
		t.Fatal("experiences leaked between users")
	}
}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	s := testutil.NewServer(t)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Heat")
	res := s.Client(t).PostForm(itemURL(item)+"/experiences", nil)
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestTodayUsesTimezone(t *testing.T) {
	now := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	if got := experiences.Today(now, "America/New_York").Format(time.DateOnly); got != "2026-02-28" {
		t.Fatalf("Today = %s", got)
	}
	if got := experiences.Today(now, "UTC").Format(time.DateOnly); got != "2026-03-01" {
		t.Fatalf("Today = %s", got)
	}
}

func TestLogFinishedWithoutDate(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	c := s.LoginAs(t, u)

	page := c.Get(itemURL(item))
	wantStatus(t, page, http.StatusOK)
	for _, want := range []string{"Finished on (optional)", "Leave blank if you don't remember.", ">Finished today<", `name="finished_today"`} {
		if !strings.Contains(page.Body, want) {
			t.Errorf("log form missing %q", want)
		}
	}
	if regexp.MustCompile(`id="f-finished_on-new"[^>]*value=`).MatchString(page.Body) {
		t.Error("finish date should be blank by default")
	}

	res := c.Form(http.MethodPost, itemURL(item)+"/experiences", url.Values{"mode": {"finished"}, "finished_on": {""}}, true)
	wantStatus(t, res, http.StatusOK)
	e := experiencesOf(t, s, u, item)[0]
	if e.State != db.ExperienceStateFinished || e.FinishedOn != nil || e.StartedOn != nil {
		t.Fatalf("undated finish: %+v", e)
	}
	if v := verbs(t, s, u); len(v) != 0 {
		t.Fatalf("undated finish recorded activity %v", v)
	}
	row := res.Body[strings.Index(res.Body, `data-experience="`+e.PublicID.String()):]
	row = row[:strings.Index(row, "</li>")]
	if !strings.Contains(row, "Finished") || strings.Contains(row, `<span class="text-sm text-muted">`) {
		t.Fatalf("undated row should show the Finished badge alone:\n%s", row)
	}
	for _, bad := range []string{"unknown", "—", "Finished on", "Finished <"} {
		if strings.Contains(row, bad) {
			t.Errorf("undated row contains %q", bad)
		}
	}
}

func TestFinishedTodayButton(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Heat")
	c := s.LoginAs(t, u)
	today := experiences.Today(time.Now(), u.Timezone).Format(time.DateOnly)

	before := time.Now()
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", url.Values{"mode": {"finished"}, "finished_on": {""}, "finished_today": {today}}), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]
	if e.FinishedOn == nil || e.FinishedOn.Format(time.DateOnly) != today {
		t.Fatalf("finished today: %+v", e)
	}
	if at := eventTime(t, s, e, "finished"); at.Before(before.Add(-time.Second)) {
		t.Fatalf("today's finish recorded at %s, want now", at)
	}
}

func TestEditClearsFinishDate(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, u)

	for _, state := range []db.ExperienceState{db.ExperienceStateFinished, db.ExperienceStateAbandoned} {
		item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Book "+string(state))
		e := createExperience(t, s, u.ID, item.ID, db.ExperienceStateActive)
		action := map[db.ExperienceState]string{db.ExperienceStateFinished: "/finish", db.ExperienceStateAbandoned: "/abandon"}[state]
		wantStatus(t, c.PostForm(expURL(e, action), nil), http.StatusSeeOther)
		if got := experiencesOf(t, s, u, item)[0]; got.FinishedOn == nil {
			t.Fatalf("%s should default to today: %+v", action, got)
		}

		edit := c.Get(expURL(e, "/edit"))
		wantStatus(t, edit, http.StatusOK)
		if regexp.MustCompile(`name="finished_on"[^>]*required`).MatchString(edit.Body) {
			t.Errorf("%s: finish date is still required on the edit page", state)
		}
		wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"started_on": {"2019-05-01"}, "visibility": {"private"}}), http.StatusSeeOther)
		got := experiencesOf(t, s, u, item)[0]
		if got.FinishedOn != nil || got.StartedOn.Format(time.DateOnly) != "2019-05-01" {
			t.Fatalf("%s: clearing the finish date: %+v", state, got)
		}

		page := c.Get(itemURL(item))
		label := map[db.ExperienceState]string{db.ExperienceStateFinished: "Finished", db.ExperienceStateAbandoned: "Abandoned"}[state]
		if !strings.Contains(page.Body, ">Started May 1, 2019<") || strings.Contains(page.Body, "· "+label) {
			t.Errorf("%s: date line after clearing:\n%s", state, page.Body)
		}

		wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"started_on": {"2019-05-03"}, "finished_on": {"2019-05-02"}, "visibility": {"private"}}), http.StatusUnprocessableEntity)
		wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"finished_on": {"2999-01-01"}, "visibility": {"private"}}), http.StatusUnprocessableEntity)
	}
	if got := strings.Join(verbs(t, s, u), ","); got != "finished,abandoned" {
		t.Fatalf("clearing a date should leave existing events alone and add none: %q", got)
	}
}

func TestEditDatingUndatedFinishRecordsEventOnce(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Jet Set Radio")
	c := s.LoginAs(t, u)
	wantStatus(t, c.PostForm(itemURL(item)+"/experiences", url.Values{"mode": {"finished"}}), http.StatusSeeOther)
	e := experiencesOf(t, s, u, item)[0]

	wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"finished_on": {"2020-02-03"}, "visibility": {"private"}}), http.StatusSeeOther)
	if at := eventTime(t, s, e, "finished"); !at.Equal(time.Date(2020, 2, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dated finish recorded at %s", at)
	}
	wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"visibility": {"private"}}), http.StatusSeeOther)
	wantStatus(t, c.PostForm(expURL(e, "/edit"), url.Values{"finished_on": {"2020-02-04"}, "visibility": {"private"}}), http.StatusSeeOther)
	if got := strings.Join(verbs(t, s, u), ","); got != "finished" {
		t.Fatalf("verbs %q", got)
	}
}

func TestSyncedPlaytimeReplacesManualHours(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	synced := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Hades")
	manual := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	acct := testutil.CreateConnectedAccount(t, s.Pool, u.ID, "steam", "765")
	exec(t, s, `insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total) values ($1, $2, '1145360', 300)`, acct.ID, synced.ID)
	c := s.LoginAs(t, u)

	for _, it := range []db.MediaItem{synced, manual} {
		wantStatus(t, c.PostForm(itemURL(it)+"/experiences", nil), http.StatusSeeOther)
	}
	page := c.Get(itemURL(synced)).Body
	e := experiencesOf(t, s, u, synced)[0]
	if strings.Contains(page, `id="f-progress-`+e.PublicID.String()) {
		t.Error("synced game should not offer manual hours entry")
	}
	if !regexp.MustCompile(`data-testid="synced-playtime"[^>]*>5 h played on (Steam|steam)<`).MatchString(page) {
		t.Error("synced game should show its Steam playtime in the history panel")
	}
	dash := c.Get("/").Body
	if !regexp.MustCompile(`data-testid="synced-playtime"[^>]*>5 h played on (Steam|steam)<`).MatchString(dash) ||
		strings.Contains(dash, `id="f-quick-`+e.PublicID.String()) {
		t.Error("dashboard quick log should show synced playtime instead of an hours input")
	}
	e = experiencesOf(t, s, u, manual)[0]
	if !strings.Contains(dash, `id="f-quick-`+e.PublicID.String()) {
		t.Error("dashboard quick log should keep manual hours for unsynced games")
	}
	if page := c.Get(itemURL(manual)).Body; !strings.Contains(page, `id="f-progress-`+e.PublicID.String()) {
		t.Error("unsynced game should keep manual hours entry")
	}
}
