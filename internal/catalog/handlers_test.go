package catalog_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func createViaForm(t *testing.T, s *testutil.Server, c *testutil.Client, v url.Values) db.MediaItem {
	t.Helper()
	res := c.PostForm("/items", v)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create item: status %d\n%s", res.StatusCode, res.Body)
	}
	loc := res.Header.Get("Location")
	id, err := uuid.Parse(strings.TrimPrefix(loc, "/items/"))
	if err != nil {
		t.Fatalf("redirect %q: %v", loc, err)
	}
	item, err := s.Q.GetMediaItemByPublicID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func itemPath(item db.MediaItem) string { return "/items/" + item.PublicID.String() }

func TestCreateEachKind(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	ctx := context.Background()

	if res := c.Get("/items/new?kind=book"); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `name="series_name"`) {
		t.Fatalf("new book form: status %d", res.StatusCode)
	}

	cases := []struct {
		kind    db.MediaKind
		values  url.Values
		creator string
		fact    string
	}{
		{db.MediaKindBook, url.Values{"title": {"The Left Hand of Darkness"}, "series_name": {"Hainish Cycle"}, "series_position": {"4"},
			"credit_name": {"Ursula K. Le Guin", ""}, "credit_role": {"author", "author"}, "genres": {"Sci-Fi, classics, sci-fi"}, "release_date": {"1969-03-01"}},
			"Ursula K. Le Guin", "Hainish Cycle #4"},
		{db.MediaKindGame, url.Values{"title": {"Shenmue"}, "franchise": {"Shenmue"}, "credit_name": {"Sega AM2"}, "credit_role": {"developer"}, "release_year": {"1999"}},
			"Sega AM2", "Franchise"},
		{db.MediaKindFilm, url.Values{"title": {"Arrival"}, "runtime_minutes": {"116"}, "credit_name": {"Denis Villeneuve"}, "credit_role": {"director"}},
			"Denis Villeneuve", "116 min"},
		{db.MediaKindShow, url.Values{"title": {"The Expanse"}, "network": {"Syfy"}, "status": {"ended"}, "season_count": {"6"}, "episode_count": {"62"},
			"credit_name": {"Mark Fergus"}, "credit_role": {"creator"}},
			"Mark Fergus", "Syfy"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			tc.values.Set("kind", string(tc.kind))
			item := createViaForm(t, s, c, tc.values)
			if item.Kind != tc.kind || item.CreatedByUserID == nil || *item.CreatedByUserID != alice.ID {
				t.Fatalf("item = %+v", item)
			}
			if item.SortTitle != catalog.SortTitle(item.Title) {
				t.Errorf("sort_title = %q", item.SortTitle)
			}
			res := c.Get(itemPath(item))
			if res.StatusCode != http.StatusOK {
				t.Fatalf("item page: %d", res.StatusCode)
			}
			for _, want := range []string{item.Title, tc.creator, tc.fact, itemPath(item) + "/cover", itemPath(item) + "/edit"} {
				if !strings.Contains(res.Body, want) {
					t.Errorf("item page missing %q", want)
				}
			}
		})
	}

	book, err := s.Q.CatalogSearch(ctx, db.CatalogSearchParams{Query: "The Left Hand of Darkness", Lim: 1})
	if err != nil || len(book) != 1 {
		t.Fatalf("find book: %v", err)
	}
	if got := book[0].Genres; !slices.Equal(got, []string{"sci-fi", "classics"}) {
		t.Errorf("genres = %v", got)
	}
	if book[0].ReleaseYear == nil || *book[0].ReleaseYear != 1969 {
		t.Errorf("release_year should derive from date, got %v", book[0].ReleaseYear)
	}
	if book[0].SortTitle != "Left Hand of Darkness, The" {
		t.Errorf("sort_title = %q", book[0].SortTitle)
	}
	show, _ := s.Q.CatalogSearch(ctx, db.CatalogSearchParams{Query: "The Expanse", Lim: 1})
	sd, err := s.Q.GetShowDetails(ctx, show[0].ID)
	if err != nil || sd.EpisodeCount == nil || *sd.EpisodeCount != 62 || sd.Status == nil || *sd.Status != "ended" {
		t.Errorf("show details = %+v, %v", sd, err)
	}
}

func TestCreateValidation(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.LoginAs(t, testutil.CreateUser(t, s.Q, "alice"))
	res := c.PostForm("/items", url.Values{"kind": {"film"}, "title": {" "}, "runtime_minutes": {"abc"}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.Contains(res.Body, "This field is required.") || !strings.Contains(res.Body, "Must be a whole number") {
		t.Errorf("missing field errors")
	}
	if res := c.PostForm("/items", url.Values{"kind": {"music"}, "title": {"X"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad kind: status %d", res.StatusCode)
	}
}

func TestCreditsReuseCreators(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.LoginAs(t, testutil.CreateUser(t, s.Q, "alice"))
	for _, title := range []string{"A Wizard of Earthsea", "The Dispossessed"} {
		createViaForm(t, s, c, url.Values{"kind": {"book"}, "title": {title}, "credit_name": {"Ursula K. Le Guin"}, "credit_role": {"author"}})
	}
	createViaForm(t, s, c, url.Values{"kind": {"book"}, "title": {"Lavinia"}, "credit_name": {"ursula k. le guin"}, "credit_role": {"author"}})
	var n int
	var sortName string
	if err := s.Pool.QueryRow(context.Background(), "select count(*), min(sort_name) from creators").Scan(&n, &sortName); err != nil {
		t.Fatal(err)
	}
	if n != 1 || sortName != "Guin, Ursula K. Le" {
		t.Errorf("creators = %d (%q), want one shared creator", n, sortName)
	}
}

func TestGameEditionsAcrossPlatforms(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.LoginAs(t, testutil.CreateUser(t, s.Q, "alice"))
	ctx := context.Background()
	game := createViaForm(t, s, c, url.Values{"kind": {"game"}, "title": {"Soulcalibur II"}})

	dc, err := s.Q.GetPlatformBySlug(ctx, "dreamcast")
	if err != nil {
		t.Fatal(err)
	}
	if res := c.Get(itemPath(game) + "/editions/new"); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "Sega Dreamcast") {
		t.Fatalf("new edition form: %d", res.StatusCode)
	}
	res := c.PostForm(itemPath(game)+"/editions", url.Values{"platform_id": {strconv.FormatInt(dc.ID, 10)}, "format": {"physical"}, "region": {"NTSC-U"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("dreamcast edition: %d\n%s", res.StatusCode, res.Body)
	}
	// No platform_id chosen: the typed search text resolves the platform (no-JS path).
	res = c.PostForm(itemPath(game)+"/editions", url.Values{"platform_q": {"PlayStation 2"}, "platform_id": {""}, "region": {"PAL"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("ps2 edition: %d\n%s", res.StatusCode, res.Body)
	}
	if res := c.PostForm(itemPath(game)+"/editions", url.Values{"format": {"bluray"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("film format on a game: status %d", res.StatusCode)
	}
	if res := c.PostForm(itemPath(game)+"/editions", url.Values{"platform_q": {"Nonexistent 3000"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown platform: status %d", res.StatusCode)
	}

	eds, err := s.Q.ListEditionsForItem(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, e := range eds {
		slugs = append(slugs, *e.PlatformSlug)
	}
	slices.Sort(slugs)
	if !slices.Equal(slugs, []string{"dreamcast", "ps2"}) {
		t.Fatalf("edition platforms = %v", slugs)
	}
	page := c.Get(itemPath(game))
	if !strings.Contains(page.Body, "Sega Dreamcast · Physical · NTSC-U") || !strings.Contains(page.Body, "PlayStation 2 · PAL") {
		t.Errorf("item page doesn't list both editions")
	}

	ps2 := eds[0]
	if *ps2.PlatformSlug != "ps2" {
		ps2 = eds[1]
	}
	editPath := itemPath(game) + "/editions/" + ps2.PublicID.String() + "/edit"
	if res := c.Get(editPath); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `value="PAL"`) {
		t.Fatalf("edit edition form: %d", res.StatusCode)
	}
	res = c.PostForm(editPath, url.Values{"platform_id": {strconv.FormatInt(*ps2.PlatformID, 10)}, "region": {"JP"}, "publisher": {"Namco"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update edition: %d", res.StatusCode)
	}
	if !strings.Contains(c.Get(itemPath(game)).Body, "PlayStation 2 · JP") {
		t.Error("edition update not shown")
	}

	if res := c.PostForm(itemPath(game)+"/editions/"+ps2.PublicID.String()+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete edition: %d", res.StatusCode)
	}
	if eds, _ := s.Q.ListEditionsForItem(ctx, game.ID); len(eds) != 1 {
		t.Errorf("after delete: %d editions", len(eds))
	}

	other := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Other")
	if res := c.Get(itemPath(other) + "/editions/" + eds[0].PublicID.String() + "/edit"); res.StatusCode != http.StatusForbidden {
		t.Errorf("edition under an item alice didn't create: %d", res.StatusCode)
	}
}

func TestDeleteEditionInUse(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	game := createViaForm(t, s, c, url.Values{"kind": {"game"}, "title": {"Jet Set Radio"}})
	ed := testutil.CreateEdition(t, s.Pool, game.ID, "dreamcast")
	entry := testutil.CreateEntry(t, s.Pool, alice.ID, game.ID)
	if _, err := s.Pool.Exec(context.Background(), "update library_entries set edition_id = $1 where id = $2", ed.ID, entry.ID); err != nil {
		t.Fatal(err)
	}
	if res := c.PostForm(itemPath(game)+"/editions/"+ed.PublicID.String()+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete in-use edition: %d", res.StatusCode)
	}
	if eds, _ := s.Q.ListEditionsForItem(context.Background(), game.ID); len(eds) != 1 {
		t.Errorf("in-use edition was deleted")
	}
}

func TestPlatformPickerFindsAliases(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.LoginAs(t, testutil.CreateUser(t, s.Q, "alice"))
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Sonic the Hedgehog")
	genesis, err := s.Q.GetPlatformBySlug(context.Background(), "genesis")
	if err != nil {
		t.Fatal(err)
	}

	matches, err := catalog.PlatformMatches(context.Background(), s.Q, "Mega Drive")
	if err != nil || len(matches) == 0 || matches[0].Slug != "genesis" {
		t.Fatalf("PlatformMatches(Mega Drive) = %v, %v", matches, err)
	}

	req, _ := http.NewRequest(http.MethodGet, s.URL+itemPath(game)+"/editions/platforms?platform_q=Mega+Drive", nil)
	req.Header.Set("HX-Request", "true")
	res := c.Do(req)
	if res.StatusCode != http.StatusOK || strings.Contains(res.Body, "<html") {
		t.Fatalf("picker partial: %d", res.StatusCode)
	}
	want := `value="` + strconv.FormatInt(genesis.ID, 10) + `" selected`
	if !strings.Contains(res.Body, want) || !strings.Contains(res.Body, "Sega Genesis") {
		t.Errorf("picker didn't select Genesis:\n%s", res.Body)
	}
}

func TestEditPermissions(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	admin := testutil.CreateUser(t, s.Q, "root", testutil.Admin)
	ca, cb, cadm := s.LoginAs(t, alice), s.LoginAs(t, bob), s.LoginAs(t, admin)

	item := createViaForm(t, s, ca, url.Values{"kind": {"game"}, "title": {"Crazy Taxi"}})
	ed := testutil.CreateEdition(t, s.Pool, item.ID, "dreamcast")
	bobsItem := createViaForm(t, s, cb, url.Values{"kind": {"film"}, "title": {"Heat"}})

	if body := cb.Get(itemPath(item)).Body; strings.Contains(body, itemPath(item)+"/edit") || strings.Contains(body, "/editions/new") {
		t.Error("bob sees edit links on alice's item")
	}
	edPath := itemPath(item) + "/editions/" + ed.PublicID.String()
	for _, tc := range []struct {
		method, path string
	}{
		{"GET", itemPath(item) + "/edit"},
		{"POST", itemPath(item) + "/edit"},
		{"GET", itemPath(item) + "/editions/new"},
		{"POST", itemPath(item) + "/editions"},
		{"GET", edPath + "/edit"},
		{"POST", edPath + "/edit"},
		{"POST", edPath + "/delete"},
	} {
		var res *testutil.Response
		if tc.method == "GET" {
			res = cb.Get(tc.path)
		} else {
			res = cb.PostForm(tc.path, url.Values{"title": {"Hijacked"}, "region": {"X"}})
		}
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("bob %s %s: status %d, want 403", tc.method, tc.path, res.StatusCode)
		}
	}
	got, _ := s.Q.GetMediaItemByID(context.Background(), item.ID)
	if got.Title != "Crazy Taxi" {
		t.Fatalf("bob changed the title to %q", got.Title)
	}
	if eds, _ := s.Q.ListEditionsForItem(context.Background(), item.ID); len(eds) != 1 || eds[0].Region != nil {
		t.Fatalf("bob changed editions: %+v", eds)
	}

	if res := cb.PostForm(itemPath(bobsItem)+"/edit", url.Values{"title": {"Heat (1995)"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("bob editing his own item: %d", res.StatusCode)
	}
	if res := cadm.Get(itemPath(item) + "/edit"); res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `value="Crazy Taxi"`) {
		t.Errorf("admin edit form: %d", res.StatusCode)
	}
	if res := cadm.PostForm(itemPath(item)+"/edit", url.Values{"title": {"Crazy Taxi 2"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("admin edit: %d", res.StatusCode)
	}
	if res := cadm.PostForm(itemPath(item)+"/editions", url.Values{"platform_q": {"PS2"}}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("admin add edition: %d", res.StatusCode)
	}
	if res := cadm.Get(itemPath(bobsItem) + "/editions/" + ed.PublicID.String() + "/edit"); res.StatusCode != http.StatusNotFound {
		t.Errorf("edition under the wrong item: %d", res.StatusCode)
	}
	if res := cb.Get("/items/" + uuid.NewString() + "/edit"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown item: %d", res.StatusCode)
	}
	if res := s.Client(t).Get("/items/new"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous new item: %d", res.StatusCode)
	}
}

func TestEditLocksProviderFields(t *testing.T) {
	s := testutil.NewServer(t)
	admin := testutil.CreateUser(t, s.Q, "root", testutil.Admin)
	c := s.LoginAs(t, admin)
	ctx := context.Background()

	item := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Blade Runner")
	if _, err := s.Pool.Exec(ctx, `update media_items set metadata_provider = 'tmdb', synopsis = 'Replicants.', release_year = 1982, locked_fields = '{genres}' where id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into film_details (media_item_id, runtime_minutes) values ($1, 117)`, item.ID); err != nil {
		t.Fatal(err)
	}

	form := c.Get(itemPath(item) + "/edit")
	if !strings.Contains(form.Body, `value="117"`) || !strings.Contains(form.Body, "Replicants.") {
		t.Fatalf("edit form not prefilled")
	}
	res := c.PostForm(itemPath(item)+"/edit", url.Values{
		"title": {"Blade Runner: The Final Cut"}, "synopsis": {"Replicants."}, "release_year": {"1982"},
		"runtime_minutes": {"117"}, "credit_name": {"Ridley Scott"}, "credit_role": {"director"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit: %d\n%s", res.StatusCode, res.Body)
	}
	got, _ := s.Q.GetMediaItemByID(ctx, item.ID)
	slices.Sort(got.LockedFields)
	if !slices.Equal(got.LockedFields, []string{"credits", "genres", "title"}) {
		t.Errorf("locked_fields = %v", got.LockedFields)
	}
	if got.SortTitle != "Blade Runner: The Final Cut" {
		t.Errorf("sort_title = %q", got.SortTitle)
	}

	res = c.PostForm(itemPath(item)+"/edit", url.Values{
		"title": {"Blade Runner: The Final Cut"}, "synopsis": {"Replicants."}, "release_year": {"1982"},
		"runtime_minutes": {"118"}, "credit_name": {"Ridley Scott"}, "credit_role": {"director"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("second edit: %d", res.StatusCode)
	}
	got, _ = s.Q.GetMediaItemByID(ctx, item.ID)
	slices.Sort(got.LockedFields)
	if !slices.Equal(got.LockedFields, []string{"credits", "genres", "runtime_minutes", "title"}) {
		t.Errorf("locked_fields after second edit = %v", got.LockedFields)
	}

	manual := createViaForm(t, s, c, url.Values{"kind": {"book"}, "title": {"Piranesi"}})
	if res := c.PostForm(itemPath(manual)+"/edit", url.Values{"title": {"Piranesi (2020)"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit manual: %d", res.StatusCode)
	}
	if got, _ := s.Q.GetMediaItemByID(ctx, manual.ID); len(got.LockedFields) != 0 {
		t.Errorf("manual item locked %v", got.LockedFields)
	}
}

func TestSearch(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.LoginAs(t, testutil.CreateUser(t, s.Q, "alice"))
	partial1 := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Halo 2")
	partial2 := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Halo: Combat Evolved Anniversary")
	exact := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Halo")
	book := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Halo: The Fall of Reach")
	testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")

	res := c.Get("/search")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `href="/items/new"`) {
		t.Fatalf("search page: %d, missing add link", res.StatusCode)
	}

	res = c.Get("/search?q=halo")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search: %d", res.StatusCode)
	}
	pos := func(it db.MediaItem) int { return strings.Index(res.Body, itemPath(it)) }
	if pos(exact) < 0 || pos(exact) > pos(partial1) || pos(exact) > pos(partial2) {
		t.Errorf("exact match not ranked first: exact=%d partial=%d,%d", pos(exact), pos(partial1), pos(partial2))
	}
	if pos(book) < 0 || !strings.Contains(res.Body, ">Book") || strings.Contains(res.Body, "Arrival") {
		t.Errorf("results not grouped by kind or contain non-matches")
	}

	req, _ := http.NewRequest(http.MethodGet, s.URL+"/search?q=halo&kind=book", nil)
	req.Header.Set("HX-Request", "true")
	res = c.Do(req)
	if strings.Contains(res.Body, "<html") || !strings.Contains(res.Body, `id="search-results"`) {
		t.Fatalf("partial search should render only results")
	}
	if strings.Contains(res.Body, itemPath(exact)) || !strings.Contains(res.Body, itemPath(book)) {
		t.Errorf("kind filter not applied")
	}

	if res := c.Get("/search?q=zzzzqqq"); !strings.Contains(res.Body, "No matches") {
		t.Errorf("empty result state missing")
	}
}

func TestChangedFields(t *testing.T) {
	five := int32(5)
	a := catalog.ItemInput{Title: "X", Genres: []string{"rpg"}, Credits: []catalog.CreditInput{{Name: "A", Role: db.CreditRoleAuthor}}}
	b := a
	b.SeasonCount = &five
	b.Credits = []catalog.CreditInput{{Name: "a", Role: db.CreditRoleAuthor}}
	if got := catalog.ChangedFields(a, b); !slices.Equal(got, []string{"season_count"}) {
		t.Errorf("ChangedFields = %v", got)
	}
}
