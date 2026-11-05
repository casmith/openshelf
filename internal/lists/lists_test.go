package lists_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func listPath(l db.List) string { return "/lists/" + l.PublicID.String() }

func itemPath(l db.List, it db.MediaItem) string {
	return listPath(l) + "/items/" + it.PublicID.String()
}

// titlesInOrder returns the list's items by position and fails unless positions are 1..n.
func titlesInOrder(t *testing.T, pool *pgxpool.Pool, listID int64) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`select li.position, m.title from list_items li join media_items m on m.id = li.media_item_id
		 where li.list_id = $1 order by li.position`, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var pos int
		var title string
		if err := rows.Scan(&pos, &title); err != nil {
			t.Fatal(err)
		}
		if pos != len(titles)+1 {
			t.Fatalf("position %d for %q, want %d (positions not contiguous)", pos, title, len(titles)+1)
		}
		titles = append(titles, title)
	}
	return titles
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantStatus(t *testing.T, res *testutil.Response, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, res.Body)
	}
}

func TestCreateMixedMediaList(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	if _, err := s.Pool.Exec(context.Background(), `update users set default_visibility = 'instance' where id = $1`, alice.ID); err != nil {
		t.Fatal(err)
	}
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")
	c := s.LoginAs(t, alice)

	res := c.Get("/lists/new")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, `<option value="instance" selected`) {
		t.Fatal("new list form should preselect the user's default visibility")
	}

	res = c.PostForm("/lists", url.Values{"name": {""}, "visibility": {"instance"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	res = c.PostForm("/lists", url.Values{"name": {"Favorites"}, "description": {"Best of all"}, "visibility": {"instance"}, "ranked": {"on"}})
	wantStatus(t, res, http.StatusSeeOther)
	loc := res.Header.Get("Location")
	list, err := s.Q.ListsGetOwned(context.Background(), db.ListsGetOwnedParams{PublicID: uuid.MustParse(strings.TrimPrefix(loc, "/lists/")), UserID: alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	if list.Visibility != db.VisibilityInstance || !list.Ranked || list.Name != "Favorites" {
		t.Fatalf("unexpected list %+v", list)
	}

	for _, it := range []db.MediaItem{game, book, film} {
		res = c.PostForm(listPath(list)+"/items", url.Values{"item": {it.PublicID.String()}})
		wantStatus(t, res, http.StatusSeeOther)
	}
	if got := titlesInOrder(t, s.Pool, list.ID); strings.Join(got, ",") != "Shenmue,Dune,Arrival" {
		t.Fatalf("items = %v", got)
	}
	if n := count(t, s.Pool, `select count(*) from activity_events where verb = 'listed' and list_id = $1 and user_id = $2 and visibility = 'instance'`, list.ID, alice.ID); n != 3 {
		t.Fatalf("listed events = %d, want 3", n)
	}

	res = c.Get(listPath(list))
	wantStatus(t, res, http.StatusOK)
	for _, title := range []string{"Shenmue", "Dune", "Arrival", "Best of all", "/edit"} {
		if !strings.Contains(res.Body, title) {
			t.Errorf("list page missing %q", title)
		}
	}
	if n := strings.Count(res.Body, "data-rank"); n != 3 {
		t.Errorf("ranked list shows %d numbers, want 3", n)
	}
	if !strings.Contains(res.Body, "/items/"+book.PublicID.String()) {
		t.Error("list page should link items by public_id")
	}

	res = c.Get("/lists")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "Favorites") || !strings.Contains(res.Body, "3 items") {
		t.Fatalf("index missing list:\n%s", res.Body)
	}
	if n := strings.Count(res.Body, "placeholder-"); n != 3 {
		t.Errorf("collage shows %d covers, want 3", n)
	}
}

func TestCreateWithItemFromItemPage(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindShow, "The Expanse")
	c := s.LoginAs(t, alice)

	res := c.Get("/lists/new?item=" + item.PublicID.String())
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, item.PublicID.String()) {
		t.Fatal("form should carry the item")
	}
	res = c.PostForm("/lists", url.Values{"name": {"Space"}, "visibility": {"private"}, "item": {item.PublicID.String()}})
	wantStatus(t, res, http.StatusSeeOther)
	if n := count(t, s.Pool, `select count(*) from list_items li join lists l on l.id = li.list_id where l.user_id = $1 and li.media_item_id = $2`, alice.ID, item.ID); n != 1 {
		t.Fatalf("item not added on create (%d)", n)
	}
}

func TestCollageUsesCoverURLs(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	var ids []int64
	for _, title := range []string{"A", "B", "C", "D", "E"} {
		it := testutil.CreateItem(t, s.Pool, db.MediaKindBook, title)
		ids = append(ids, it.ID)
	}
	var imageID int64
	if err := s.Pool.QueryRow(context.Background(), `insert into images (source_url) values ('https://example.com/a.jpg') returning id`).Scan(&imageID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(context.Background(), `update media_items set cover_image_id = $1 where id = $2`, imageID, ids[0]); err != nil {
		t.Fatal(err)
	}
	testutil.CreateList(t, s.Pool, alice.ID, "Five", db.VisibilityPrivate, ids...)
	res := s.LoginAs(t, alice).Get("/lists")
	wantStatus(t, res, http.StatusOK)
	covers := strings.Count(res.Body, "/covers/") + strings.Count(res.Body, "placeholder-book")
	if covers != 4 {
		t.Fatalf("collage has %d covers, want the first 4", covers)
	}
	if !strings.Contains(res.Body, "/covers/"+strconv.FormatInt(imageID, 10)+"/thumb") {
		t.Fatal("collage should use the first item's cover")
	}
}

func TestMoveAndRemoveKeepPositionsContiguous(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	var items []db.MediaItem
	var ids []int64
	for _, title := range []string{"A", "B", "C", "D"} {
		it := testutil.CreateItem(t, s.Pool, db.MediaKindGame, title)
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	list := testutil.CreateList(t, s.Pool, alice.ID, "Order", db.VisibilityPrivate, ids...)
	c := s.LoginAs(t, alice)

	steps := []struct {
		item int
		to   string
		want string
	}{
		{2, "up", "A,C,B,D"},
		{3, "top", "D,A,C,B"},
		{3, "down", "A,D,C,B"},
		{0, "bottom", "D,C,B,A"},
		{0, "down", "D,C,B,A"}, // already last
		{3, "up", "D,C,B,A"},   // already first
	}
	for _, st := range steps {
		res := c.PostForm(itemPath(list, items[st.item])+"/move", url.Values{"to": {st.to}})
		wantStatus(t, res, http.StatusSeeOther)
		if got := strings.Join(titlesInOrder(t, s.Pool, list.ID), ","); got != st.want {
			t.Fatalf("move %s %s: got %s, want %s", items[st.item].Title, st.to, got, st.want)
		}
	}

	res := c.PostForm(itemPath(list, items[0])+"/move", url.Values{"to": {"sideways"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	res = c.Form(http.MethodPost, itemPath(list, items[1])+"/move", url.Values{"to": {"top"}}, true)
	wantStatus(t, res, http.StatusOK)
	if !strings.HasPrefix(strings.TrimSpace(res.Body), `<div id="list-items">`) || strings.Contains(res.Body, "<html") {
		t.Fatalf("htmx move should return only the items fragment:\n%s", res.Body)
	}
	if got := strings.Join(titlesInOrder(t, s.Pool, list.ID), ","); got != "B,D,C,A" {
		t.Fatalf("got %s", got)
	}

	res = c.Form(http.MethodDelete, itemPath(list, items[3]), nil, true)
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(res.Body, ">D<") {
		t.Fatal("removed item still rendered")
	}
	if got := strings.Join(titlesInOrder(t, s.Pool, list.ID), ","); got != "B,C,A" {
		t.Fatalf("after remove got %s", got)
	}

	res = c.PostForm(itemPath(list, items[1])+"/delete", nil)
	wantStatus(t, res, http.StatusSeeOther)
	if got := strings.Join(titlesInOrder(t, s.Pool, list.ID), ","); got != "C,A" {
		t.Fatalf("after remove got %s", got)
	}
	res = c.PostForm(itemPath(list, items[1])+"/delete", nil)
	wantStatus(t, res, http.StatusNotFound)
}

func TestDuplicateAddIsNoop(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Alien")
	list := testutil.CreateList(t, s.Pool, alice.ID, "Horror", db.VisibilityPrivate)
	c := s.LoginAs(t, alice)
	next := "/items/" + item.PublicID.String()

	for range 2 {
		res := c.PostForm(listPath(list)+"/items", url.Values{"item": {item.PublicID.String()}, "next": {next}})
		wantStatus(t, res, http.StatusSeeOther)
		if res.Header.Get("Location") != next {
			t.Fatalf("redirect to %q, want %q", res.Header.Get("Location"), next)
		}
	}
	if n := count(t, s.Pool, `select count(*) from list_items where list_id = $1`, list.ID); n != 1 {
		t.Fatalf("list has %d items, want 1", n)
	}
	if n := count(t, s.Pool, `select count(*) from activity_events where list_id = $1`, list.ID); n != 1 {
		t.Fatalf("activity events = %d, want 1", n)
	}
	res := c.Get(listPath(list))
	if !strings.Contains(res.Body, "is already on") {
		t.Fatal("duplicate add should flash a message")
	}
}

func TestItemPanel(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	testutil.CreateList(t, s.Pool, alice.ID, "Has it", db.VisibilityPrivate, item.ID)
	other := testutil.CreateList(t, s.Pool, alice.ID, "Wants it", db.VisibilityPrivate)
	testutil.CreateList(t, s.Pool, bob.ID, "Bob secret", db.VisibilityInstance)

	res := s.LoginAs(t, alice).Get("/items/" + item.PublicID.String())
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "Has it") || !strings.Contains(res.Body, "Add to list") {
		t.Fatal("panel missing lists")
	}
	if !strings.Contains(res.Body, `action="`+listPath(other)+`/items"`) {
		t.Fatal("panel should offer lists without the item")
	}
	if strings.Contains(res.Body, "Bob secret") {
		t.Fatal("panel shows another user's list")
	}
}

func TestEditAndDelete(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	list := testutil.CreateList(t, s.Pool, alice.ID, "Old", db.VisibilityPrivate)
	c := s.LoginAs(t, alice)

	wantStatus(t, c.Get(listPath(list)+"/edit"), http.StatusOK)
	wantStatus(t, c.PostForm(listPath(list)+"/edit", url.Values{"name": {"New"}, "visibility": {"bogus"}}), http.StatusUnprocessableEntity)
	wantStatus(t, c.PostForm(listPath(list)+"/edit", url.Values{"name": {"New"}, "visibility": {"public"}}), http.StatusSeeOther)
	got, err := s.Q.ListsGetOwned(context.Background(), db.ListsGetOwnedParams{PublicID: list.PublicID, UserID: alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New" || got.Visibility != db.VisibilityPublic || got.Ranked {
		t.Fatalf("not updated: %+v", got)
	}
	wantStatus(t, c.PostForm(listPath(list)+"/delete", nil), http.StatusSeeOther)
	if n := count(t, s.Pool, `select count(*) from lists where id = $1`, list.ID); n != 0 {
		t.Fatal("list not deleted")
	}
}

func TestCrossUserIsolation(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	a := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Alpha")
	b := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Beta")
	secret := testutil.CreateList(t, s.Pool, bob.ID, "Bob private", db.VisibilityPrivate, a.ID, b.ID)
	c := s.LoginAs(t, alice)

	for _, res := range []*testutil.Response{
		c.Get(listPath(secret)),
		c.Get(listPath(secret) + "/edit"),
		c.PostForm(listPath(secret)+"/edit", url.Values{"name": {"Pwned"}, "visibility": {"public"}}),
		c.PostForm(listPath(secret)+"/delete", nil),
		c.Form(http.MethodDelete, listPath(secret), nil, false),
		c.PostForm(listPath(secret)+"/items", url.Values{"item": {a.PublicID.String()}}),
		c.PostForm(itemPath(secret, b)+"/move", url.Values{"to": {"top"}}),
		c.PostForm(itemPath(secret, a)+"/delete", nil),
		c.Form(http.MethodDelete, itemPath(secret, a), nil, true),
		s.Client(t).Get(listPath(secret)),
	} {
		wantStatus(t, res, http.StatusNotFound)
	}
	for _, path := range []string{"/lists", "/lists?tab=shared"} {
		if res := c.Get(path); strings.Contains(res.Body, "Bob private") {
			t.Fatalf("%s leaks a private list", path)
		}
	}
	if got := strings.Join(titlesInOrder(t, s.Pool, secret.ID), ","); got != "Alpha,Beta" {
		t.Fatalf("bob's list changed: %s", got)
	}
	if n := count(t, s.Pool, `select count(*) from lists where id = $1 and name = 'Bob private' and visibility = 'private'`, secret.ID); n != 1 {
		t.Fatal("bob's list was modified")
	}
}

func TestSharedListsReadableNotEditable(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	a := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Alpha")
	b := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Beta")
	inst := testutil.CreateList(t, s.Pool, bob.ID, "Bob instance", db.VisibilityInstance, a.ID)
	pub := testutil.CreateList(t, s.Pool, bob.ID, "Bob public", db.VisibilityPublic, a.ID)
	c := s.LoginAs(t, alice)

	res := c.Get(listPath(inst))
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "Alpha") || strings.Contains(res.Body, "/edit") || strings.Contains(res.Body, "/move") {
		t.Fatal("instance list should render read-only for another user")
	}
	for _, res := range []*testutil.Response{
		c.Get(listPath(inst) + "/edit"),
		c.PostForm(listPath(inst)+"/edit", url.Values{"name": {"Pwned"}, "visibility": {"public"}}),
		c.PostForm(listPath(inst)+"/delete", nil),
		c.PostForm(listPath(inst)+"/items", url.Values{"item": {b.PublicID.String()}}),
		c.PostForm(itemPath(inst, a)+"/move", url.Values{"to": {"top"}}),
		c.PostForm(itemPath(inst, a)+"/delete", nil),
	} {
		wantStatus(t, res, http.StatusNotFound)
	}
	if got := strings.Join(titlesInOrder(t, s.Pool, inst.ID), ","); got != "Alpha" {
		t.Fatalf("instance list changed: %s", got)
	}

	res = c.Get("/lists?tab=shared")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "Bob instance") || !strings.Contains(res.Body, "Bob public") || !strings.Contains(res.Body, `href="/@bob"`) {
		t.Fatal("shared tab should list bob's instance and public lists")
	}
	if res := c.Get("/lists"); strings.Contains(res.Body, "Bob instance") {
		t.Fatal("your lists tab shows another user's list")
	}

	anon := s.Client(t)
	wantStatus(t, anon.Get(listPath(inst)), http.StatusNotFound)
	wantStatus(t, anon.Get(listPath(pub)), http.StatusOK)
	if res := anon.Get("/lists"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous /lists status %d, want login redirect", res.StatusCode)
	}
}
