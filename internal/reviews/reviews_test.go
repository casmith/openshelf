package reviews_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/reviews"
	"github.com/casmith/openshelf/internal/testutil"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func ptr(v int16) *int16 { return &v }

func setDisplay(t *testing.T, s *testutil.Server, u db.User, d db.RatingDisplay) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), "update users set rating_display = $1 where id = $2", d, u.ID); err != nil {
		t.Fatal(err)
	}
}

func ownReview(t *testing.T, s *testutil.Server, userID, itemID int64) db.Review {
	t.Helper()
	r, err := s.Q.ReviewsGetOwnForItem(context.Background(), db.ReviewsGetOwnForItemParams{UserID: userID, MediaItemID: itemID})
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	return r
}

// testutil.CreateExperience fails on Postgres (untyped $3 used as text and enum).
func finishedExperience(t *testing.T, s *testutil.Server, userID, itemID int64) db.Experience {
	t.Helper()
	var e db.Experience
	err := s.Pool.QueryRow(context.Background(),
		`insert into experiences (user_id, media_item_id, state, started_on, finished_on)
		 values ($1, $2, 'finished', current_date, current_date) returning id, public_id`, userID, itemID).Scan(&e.ID, &e.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func verbs(t *testing.T, s *testutil.Server, reviewID int64) []string {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(), "select verb::text || ':' || visibility::text from activity_events where review_id = $1 order by id", reviewID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestRatingConversions(t *testing.T) {
	if got := reviews.Stars(90); got != 4.5 {
		t.Errorf("Stars(90) = %v", got)
	}
	for _, tc := range []struct {
		d    db.RatingDisplay
		r    int16
		want string
	}{
		{db.RatingDisplayStars5, 90, "4.5/5"},
		{db.RatingDisplayStars5, 87, "4.5/5"},
		{db.RatingDisplayStars5, 80, "4/5"},
		{db.RatingDisplayTen, 90, "9/10"},
		{db.RatingDisplayTen, 85, "9/10"},
		{db.RatingDisplayHundred, 90, "90/100"},
	} {
		if got := reviews.FormatRating(tc.d, tc.r); got != tc.want {
			t.Errorf("FormatRating(%s, %d) = %q, want %q", tc.d, tc.r, got, tc.want)
		}
	}
	if v, err := reviews.ParseRating(""); v != nil || err != nil {
		t.Errorf("empty rating: %v %v", v, err)
	}
	for _, bad := range []string{"-1", "101", "x"} {
		if _, err := reviews.ParseRating(bad); err == nil {
			t.Errorf("ParseRating(%q) accepted", bad)
		}
	}
}

func TestRatingComponents(t *testing.T) {
	stars := render(t, reviews.RatingInput("rating", db.RatingDisplayStars5, ptr(90)))
	for _, want := range []string{`<fieldset>`, `<legend`, `type="radio"`, `value="90" class="sr-only" checked`, `4.5 stars`, `No rating`} {
		if !strings.Contains(stars, want) {
			t.Errorf("star input missing %q:\n%s", want, stars)
		}
	}
	if n := strings.Count(stars, `type="radio"`); n != 11 {
		t.Errorf("star input has %d radios, want 11", n)
	}
	ten := render(t, reviews.RatingInput("rating", db.RatingDisplayTen, ptr(90)))
	if !strings.Contains(ten, `<select`) || !strings.Contains(ten, `<option value="90" selected>9/10</option>`) {
		t.Errorf("ten input:\n%s", ten)
	}
	hundred := render(t, reviews.RatingInput("rating", db.RatingDisplayHundred, ptr(90)))
	if !strings.Contains(hundred, `type="number"`) || !strings.Contains(hundred, `value="90"`) {
		t.Errorf("hundred input:\n%s", hundred)
	}
	if got := render(t, reviews.RatingDisplay(db.RatingDisplayStars5, ptr(90))); !strings.Contains(got, "4.5 out of 5 stars") {
		t.Errorf("stars display: %s", got)
	}
	if got := render(t, reviews.RatingDisplay(db.RatingDisplayTen, nil)); got != "" {
		t.Errorf("nil rating rendered %q", got)
	}
}

func TestFourAndHalfStarsAcrossDisplays(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	carol := testutil.CreateUser(t, s.Q, "carol")
	setDisplay(t, s, bob, db.RatingDisplayTen)
	setDisplay(t, s, carol, db.RatingDisplayHundred)
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	path := "/items/" + item.PublicID.String() + "/review"

	ac := s.LoginAs(t, alice)
	if res := ac.Get(path); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `value="90"`) {
		t.Fatalf("form: %d", res.StatusCode)
	}
	res := ac.PostForm(path, url.Values{"rating": {"90"}, "visibility": {"instance"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", res.StatusCode, res.Body)
	}
	rev := ownReview(t, s, alice.ID, item.ID)
	if rev.Rating == nil || *rev.Rating != 90 {
		t.Fatalf("stored rating = %v, want 90", rev.Rating)
	}
	if loc := res.Header.Get("Location"); loc != "/reviews/"+rev.PublicID.String() {
		t.Errorf("redirect = %q", loc)
	}

	revPath := "/reviews/" + rev.PublicID.String()
	if res := ac.Get(revPath); !strings.Contains(res.Body, "4.5 out of 5 stars") {
		t.Errorf("alice view lacks stars: %d", res.StatusCode)
	}
	if res := s.LoginAs(t, bob).Get(revPath); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "9/10") {
		t.Errorf("bob view: %d, missing 9/10", res.StatusCode)
	}
	if res := s.LoginAs(t, carol).Get(revPath); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "90/100") {
		t.Errorf("carol view: %d, missing 90/100", res.StatusCode)
	}
}

func TestSpoilerReviewRendersCollapsed(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Primer")
	ac := s.LoginAs(t, alice)
	body := "First paragraph <script>alert(1)</script>\n\nThe twist is the ending."
	res := ac.PostForm("/items/"+item.PublicID.String()+"/review", url.Values{"body": {body}, "spoilers": {"on"}, "visibility": {"private"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d\n%s", res.StatusCode, res.Body)
	}
	for _, p := range []string{res.Header.Get("Location"), "/items/" + item.PublicID.String()} {
		page := ac.Get(p)
		if page.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", p, page.StatusCode)
		}
		if !strings.Contains(page.Body, "<details") || strings.Contains(page.Body, "<details open") {
			t.Errorf("%s: spoiler review isn't collapsed", p)
		}
		if strings.Contains(page.Body, "<script>alert") || !strings.Contains(page.Body, "&lt;script&gt;") {
			t.Errorf("%s: body not escaped", p)
		}
		if strings.Count(page.Body, `<p class="whitespace-pre-line">`) != 2 {
			t.Errorf("%s: want 2 paragraphs", p)
		}
	}
}

func TestVisibility(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item1 := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Anathem")
	item2 := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	inst := testutil.CreateReview(t, s.Pool, alice.ID, item1.ID, 80, db.VisibilityInstance)
	priv := testutil.CreateReview(t, s.Pool, alice.ID, item2.ID, 40, db.VisibilityPrivate)

	bc := s.LoginAs(t, bob)
	if res := bc.Get("/reviews/" + inst.PublicID.String()); res.StatusCode != http.StatusOK {
		t.Errorf("instance review for bob: %d, want 200", res.StatusCode)
	}
	if res := bc.Get("/reviews/" + priv.PublicID.String()); res.StatusCode != http.StatusNotFound {
		t.Errorf("private review for bob: %d, want 404", res.StatusCode)
	}
	if res := s.LoginAs(t, alice).Get("/reviews/" + priv.PublicID.String()); res.StatusCode != http.StatusOK {
		t.Errorf("private review for owner: %d", res.StatusCode)
	}
	if res := s.Client(t).Get("/reviews/" + inst.PublicID.String()); res.StatusCode != http.StatusSeeOther {
		t.Errorf("logged out: %d, want redirect to login", res.StatusCode)
	}
	if res := bc.Get("/reviews/not-a-uuid"); res.StatusCode != http.StatusNotFound {
		t.Errorf("bad id: %d", res.StatusCode)
	}

	// Item panel: bob sees alice's instance review but not her private one.
	if res := bc.Get("/items/" + item1.PublicID.String()); !strings.Contains(res.Body, inst.PublicID.String()) {
		t.Errorf("item panel hides instance review")
	}
	if res := bc.Get("/items/" + item2.PublicID.String()); strings.Contains(res.Body, priv.PublicID.String()) || strings.Contains(res.Body, "Alice") {
		t.Errorf("item panel leaks private review")
	}
}

func TestCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Jet Set Radio")
	rev := testutil.CreateReview(t, s.Pool, alice.ID, item.ID, 70, db.VisibilityInstance)
	aliceExp := finishedExperience(t, s, alice.ID, item.ID)
	bc := s.LoginAs(t, bob)

	if res := bc.Form(http.MethodDelete, "/reviews/"+rev.PublicID.String(), nil, true); res.StatusCode != http.StatusNotFound {
		t.Errorf("bob DELETE: %d, want 404", res.StatusCode)
	}
	if res := bc.PostForm("/reviews/"+rev.PublicID.String()+"/delete", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("bob POST delete: %d, want 404", res.StatusCode)
	}
	if res := bc.Get("/reviews/" + rev.PublicID.String()); strings.Contains(res.Body, "Delete review") {
		t.Errorf("bob sees owner actions")
	}
	path := "/items/" + item.PublicID.String() + "/review"
	if res := bc.Get(path); strings.Contains(res.Body, aliceExp.PublicID.String()) || strings.Contains(res.Body, `value="70" class="sr-only" checked`) {
		t.Errorf("bob's form shows alice's data")
	}
	if res := bc.PostForm(path, url.Values{"rating": {"20"}, "visibility": {"private"}, "experience": {aliceExp.PublicID.String()}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("linking alice's experience: %d, want 422", res.StatusCode)
	}
	if res := bc.PostForm(path, url.Values{"rating": {"20"}, "visibility": {"private"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("bob save: %d", res.StatusCode)
	}
	if got := ownReview(t, s, alice.ID, item.ID); got.Rating == nil || *got.Rating != 70 || got.ID != rev.ID {
		t.Errorf("alice's review changed: %+v", got)
	}
	if got := ownReview(t, s, bob.ID, item.ID); *got.Rating != 20 {
		t.Errorf("bob's review = %v", got.Rating)
	}
	if res := s.LoginAs(t, alice).Get("/items/" + item.PublicID.String()); strings.Contains(res.Body, "Bob") {
		t.Errorf("alice sees bob's private review")
	}
}

func TestUpsertActivityAndDelete(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindShow, "The Expanse")
	exp := finishedExperience(t, s, alice.ID, item.ID)
	ac := s.LoginAs(t, alice)
	path := "/items/" + item.PublicID.String() + "/review"

	if res := ac.PostForm(path, url.Values{"visibility": {"private"}}); res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "Add a rating") {
		t.Fatalf("empty review: %d", res.StatusCode)
	}
	if res := ac.PostForm(path, url.Values{"rating": {"150"}, "visibility": {"private"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("out of range: %d", res.StatusCode)
	}

	ac.PostForm(path, url.Values{"rating": {"60"}, "visibility": {"private"}})
	rev := ownReview(t, s, alice.ID, item.ID)
	ac.PostForm(path, url.Values{"rating": {"60"}, "body": {"Great."}, "visibility": {"private"}, "experience": {exp.PublicID.String()}})
	ac.PostForm(path, url.Values{"rating": {"100"}, "body": {"Great!"}, "visibility": {"instance"}, "experience": {exp.PublicID.String()}})

	got := ownReview(t, s, alice.ID, item.ID)
	if got.ID != rev.ID || *got.Rating != 100 || *got.Body != "Great!" || got.ExperienceID == nil || *got.ExperienceID != exp.ID {
		t.Fatalf("upsert result: %+v", got)
	}
	var n int
	_ = s.Pool.QueryRow(context.Background(), "select count(*) from reviews where user_id = $1", alice.ID).Scan(&n)
	if n != 1 {
		t.Errorf("reviews = %d, want 1", n)
	}
	want := []string{"rated:instance", "reviewed:instance", "rated:instance"}
	if v := verbs(t, s, rev.ID); strings.Join(v, ",") != strings.Join(want, ",") {
		t.Errorf("activity = %v, want %v", v, want)
	}
	if res := ac.Get(path); !strings.Contains(res.Body, `selected>Finished`) || !strings.Contains(res.Body, "Great!") {
		t.Errorf("edit form not prefilled")
	}

	res := ac.Form(http.MethodDelete, "/reviews/"+rev.PublicID.String(), nil, true)
	if res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") != "/items/"+item.PublicID.String() {
		t.Fatalf("delete: %d %q", res.StatusCode, res.Header.Get("HX-Redirect"))
	}
	if res := ac.Get("/reviews/" + rev.PublicID.String()); res.StatusCode != http.StatusNotFound {
		t.Errorf("deleted review: %d", res.StatusCode)
	}
	if v := verbs(t, s, rev.ID); len(v) != 0 {
		t.Errorf("activity left after delete: %v", v)
	}
}
