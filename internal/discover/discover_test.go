package discover_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
	"github.com/casmith/openshelf/internal/testutil"
)

func i32(v int32) *int32 { return &v }

func bookItem(title string) *providers.Item {
	return &providers.Item{
		Kind: db.MediaKindBook, Title: title, ReleaseYear: 1969, Synopsis: "Winter is cold.",
		Genres:   []string{"Science Fiction", "gender"},
		CoverURL: "https://covers.example/b/1-L.jpg",
		Credits:  []providers.Credit{{Name: "Ursula K. Le Guin", Role: db.CreditRoleAuthor}},
		Editions: []providers.Edition{
			{Format: db.EditionFormatPaperback, Publisher: "Ace", PageCount: i32(304),
				ExternalIDs: []providers.ExternalID{{Provider: "openlibrary_edition", Value: "OL1M"}, {Provider: "isbn13", Value: "9780441478125"}}},
			{Format: db.EditionFormatHardcover, ExternalIDs: []providers.ExternalID{{Provider: "isbn13", Value: "9780802767059"}}},
		},
		ExternalIDs: []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL59800W", URL: "https://openlibrary.org/works/OL59800W"}},
		Book:        &providers.BookDetails{},
	}
}

func newFake(name string, items ...*providers.Item) *providertest.Fake {
	f := &providertest.Fake{ProviderName: name, Kind: db.MediaKindBook, Items: map[string]*providers.Item{}}
	for _, it := range items {
		f.Kind = it.Kind
		f.Items[it.ExternalIDs[0].Value] = it
	}
	return f
}

func count(t *testing.T, s *testutil.Server, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func importForm(provider, kind, ns, id string) url.Values {
	return url.Values{"provider": {provider}, "kind": {kind}, "ns": {ns}, "id": {id}, "q": {"left hand"}}
}

func TestImportSameWorkTwiceYieldsOneItem(t *testing.T) {
	s := testutil.NewServer(t)
	s.Deps.Providers = providers.NewRegistry(newFake("openlibrary", bookItem("The Left Hand of Darkness")))
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)

	res := c.PostForm("/discover/import", importForm("openlibrary", "book", "openlibrary_work", "OL59800W"))
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/items/") {
		t.Fatalf("import: %d %s\n%s", res.StatusCode, res.Header.Get("Location"), res.Body)
	}
	loc := res.Header.Get("Location")
	res = c.PostForm("/discover/import", importForm("openlibrary", "book", "openlibrary_work", "OL59800W"))
	if res.Header.Get("Location") != loc {
		t.Fatalf("second import went to %q, want %q", res.Header.Get("Location"), loc)
	}
	// The importer itself is idempotent too, not just the handler's pre-check.
	if _, err := discover.Import(context.Background(), s.Deps, "openlibrary", bookItem("The Left Hand of Darkness"), nil); err != nil {
		t.Fatal(err)
	}

	if n := count(t, s, "select count(*) from media_items"); n != 1 {
		t.Fatalf("media_items = %d, want 1", n)
	}
	var m db.MediaItem
	err := s.Pool.QueryRow(context.Background(),
		`select title, sort_title, release_year, genres, cover_image_id, metadata_provider, metadata_refreshed_at, created_by_user_id from media_items`).
		Scan(&m.Title, &m.SortTitle, &m.ReleaseYear, &m.Genres, &m.CoverImageID, &m.MetadataProvider, &m.MetadataRefreshedAt, &m.CreatedByUserID)
	if err != nil {
		t.Fatal(err)
	}
	if m.SortTitle != "Left Hand of Darkness, The" || *m.ReleaseYear != 1969 || m.CoverImageID == nil ||
		*m.MetadataProvider != "openlibrary" || m.MetadataRefreshedAt == nil || *m.CreatedByUserID != alice.ID {
		t.Errorf("item = %+v", m)
	}
	if strings.Join(m.Genres, ",") != "science fiction,gender" {
		t.Errorf("genres = %v", m.Genres)
	}
	if n := count(t, s, "select count(*) from images where source_url = 'https://covers.example/b/1-L.jpg'"); n != 1 {
		t.Errorf("cover images = %d", n)
	}
	for sql, want := range map[string]int{
		"select count(*) from editions":                                              2,
		"select count(*) from book_details":                                          1,
		"select count(*) from credits where role = 'author'":                         1,
		"select count(*) from creators":                                              1,
		"select count(*) from external_ids where media_item_id is not null":          1,
		"select count(*) from external_ids where edition_id is not null":             3,
		"select count(*) from external_ids where provider = 'isbn13'":                2,
		"select count(*) from editions where page_count = 304 and publisher = 'Ace'": 1,
	} {
		if n := count(t, s, sql); n != want {
			t.Errorf("%s = %d, want %d", sql, n, want)
		}
	}
}

func TestRefreshKeepsLockedTitle(t *testing.T) {
	s := testutil.NewServer(t)
	fake := newFake("openlibrary", bookItem("The Left Hand of Darkness"))
	s.Deps.Providers = providers.NewRegistry(fake)
	admin := testutil.CreateUser(t, s.Q, "admin", testutil.Admin)
	member := testutil.CreateUser(t, s.Q, "bob")

	item, err := discover.Import(context.Background(), s.Deps, "openlibrary", fake.Items["OL59800W"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(context.Background(),
		`update media_items set title = 'Left Hand (My Edit)', locked_fields = '{title}', metadata_refreshed_at = now() - interval '40 days' where id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	updated := bookItem("The Left Hand of Darkness (Revised)")
	updated.Synopsis = "New synopsis."
	updated.Credits = append(updated.Credits, providers.Credit{Name: "Jo Walton", Role: db.CreditRoleOther})
	fake.Items["OL59800W"] = updated

	path := "/items/" + item.PublicID.String() + "/refresh"
	if res := s.LoginAs(t, member).PostForm(path, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member refresh: %d", res.StatusCode)
	}
	res := s.LoginAs(t, admin).PostForm(path, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/items/"+item.PublicID.String() {
		t.Fatalf("refresh: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	got, err := s.Q.GetMediaItemByID(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Left Hand (My Edit)" {
		t.Errorf("locked title overwritten: %q", got.Title)
	}
	if got.Synopsis == nil || *got.Synopsis != "New synopsis." {
		t.Errorf("unlocked synopsis not refreshed: %v", got.Synopsis)
	}
	if got.MetadataRefreshedAt == nil || time.Since(*got.MetadataRefreshedAt) > time.Hour {
		t.Errorf("metadata_refreshed_at = %v", got.MetadataRefreshedAt)
	}
	if n := count(t, s, "select count(*) from credits where media_item_id = $1", item.ID); n != 2 {
		t.Errorf("credits = %d, want 2", n)
	}
	if n := count(t, s, "select count(*) from editions"); n != 2 {
		t.Errorf("editions duplicated on refresh: %d", n)
	}

	manual := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Handwritten")
	res = s.LoginAs(t, admin).PostForm("/items/"+manual.PublicID.String()+"/refresh", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("manual item refresh: %d", res.StatusCode)
	}
}

// blockingProvider ignores its context, like a misbehaving client.
type blockingProvider struct {
	name    string
	release chan struct{}
}

func (b *blockingProvider) Name() string          { return b.name }
func (b *blockingProvider) Kinds() []db.MediaKind { return []db.MediaKind{db.MediaKindBook} }
func (b *blockingProvider) Configured() bool      { return true }
func (b *blockingProvider) Search(ctx context.Context, kind db.MediaKind, q string) ([]providers.SearchResult, error) {
	<-b.release
	return nil, nil
}
func (b *blockingProvider) Fetch(ctx context.Context, kind db.MediaKind, id string) (*providers.Item, error) {
	return nil, providers.ErrNotFound
}

func TestSlowAndFailingProvidersDontBlockResults(t *testing.T) {
	old := discover.SearchTimeout
	discover.SearchTimeout = 200 * time.Millisecond
	t.Cleanup(func() { discover.SearchTimeout = old })

	s := testutil.NewServer(t)
	slow := &blockingProvider{name: "slowpoke", release: make(chan struct{})}
	t.Cleanup(func() { close(slow.release) })
	broken := newFake("brokenlib")
	broken.Err = errors.New("boom")
	good := newFake("goodlib", bookItem("The Left Hand of Darkness"))
	other := bookItem("Left Hand Path")
	other.ExternalIDs = []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL2W"}}
	good.Items["OL2W"] = other
	s.Deps.Providers = providers.NewRegistry(slow, broken, good)

	testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Left Hand Local")
	if _, err := discover.Import(context.Background(), s.Deps, "goodlib", other, nil); err != nil {
		t.Fatal(err)
	}
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)

	start := time.Now()
	res := c.Get("/discover?kind=book&q=left+hand")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("page took %v", elapsed)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for _, want := range []string{
		"Left Hand Local", "In your catalog",
		"From goodlib", "The Left Hand of Darkness", `name="id" value="OL59800W"`,
		"slowpoke took too long to respond.", "brokenlib is unavailable right now.",
		"In catalog",
	} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(res.Body, `name="id" value="OL2W"`) {
		t.Error("already-imported result still offers Import")
	}

	req, _ := http.NewRequest(http.MethodGet, s.URL+"/discover?kind=book&q=left+hand", nil)
	req.Header.Set("HX-Request", "true")
	part := c.Do(req)
	if strings.Contains(part.Body, "<html") || !strings.Contains(part.Body, "From goodlib") {
		t.Errorf("partial response:\n%s", part.Body)
	}

	admin := testutil.CreateUser(t, s.Q, "root", testutil.Admin)
	page := s.LoginAs(t, admin).Get("/admin/providers")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Body, "boom") || !strings.Contains(page.Body, "deadline exceeded") {
		t.Errorf("admin providers page:\n%s", page.Body)
	}
	if res := c.Get("/admin/providers"); res.StatusCode != http.StatusForbidden {
		t.Errorf("member /admin/providers: %d", res.StatusCode)
	}
}

func TestAdminProvidersShowsConfiguration(t *testing.T) {
	s := testutil.NewServer(t)
	off := newFake("offlib")
	off.Unconfigured = true
	s.Deps.Providers = providers.NewRegistry(newFake("onlib"), off)
	admin := testutil.CreateUser(t, s.Q, "root", testutil.Admin)
	body := s.LoginAs(t, admin).Get("/admin/providers").Body
	on := strings.Index(body, "onlib")
	offAt := strings.Index(body, "offlib")
	if on < 0 || offAt < 0 || !strings.Contains(body[on:offAt], ">Configured<") || !strings.Contains(body[offAt:], "Not configured") {
		t.Errorf("admin providers:\n%s", body)
	}
}

func TestGamePlatformResolution(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	game := &providers.Item{
		Kind: db.MediaKindGame, Title: "Soulcalibur", ReleaseYear: 1999,
		ExternalIDs: []providers.ExternalID{{Provider: "igdb_game", Value: "1001"}},
		Game:        &providers.GameDetails{Franchise: "Soulcalibur"},
		Editions: []providers.Edition{
			{Format: db.EditionFormatPhysical, Platform: &providers.PlatformRef{ExternalID: &providers.ExternalID{Provider: "igdb_platform", Value: "23"}, Name: "Dreamcast"},
				ExternalIDs: []providers.ExternalID{{Provider: "igdb_release", Value: "a"}}},
			{Platform: &providers.PlatformRef{Name: "PS2"}, ExternalIDs: []providers.ExternalID{{Provider: "igdb_release", Value: "b"}}},
			{Platform: &providers.PlatformRef{ExternalID: &providers.ExternalID{Provider: "igdb_platform", Value: "999"}, Name: "Imaginary Console"},
				ExternalIDs: []providers.ExternalID{{Provider: "igdb_release", Value: "c"}}},
		},
	}
	item, err := discover.Import(ctx, s.Deps, "igdb", game, nil)
	if err != nil {
		t.Fatal(err)
	}
	platformOf := func(release string) string {
		var slug *string
		err := s.Pool.QueryRow(ctx, `select p.slug from editions e join external_ids x on x.edition_id = e.id
			left join platforms p on p.id = e.platform_id where x.provider = 'igdb_release' and x.value = $1`, release).Scan(&slug)
		if err != nil {
			t.Fatalf("edition %s: %v", release, err)
		}
		if slug == nil {
			return ""
		}
		return *slug
	}
	if got := platformOf("a"); got != "dreamcast" {
		t.Errorf("edition a platform = %q", got)
	}
	if got := platformOf("b"); got != "ps2" {
		t.Errorf("edition b platform = %q", got)
	}
	if got := platformOf("c"); got != "" {
		t.Errorf("unknown platform resolved to %q", got)
	}
	if n := count(t, s, `select count(*) from external_ids x join platforms p on p.id = x.platform_id
		where x.provider = 'igdb_platform' and x.value = '23' and p.slug = 'dreamcast'`); n != 1 {
		t.Error("igdb_platform 23 not recorded on Dreamcast")
	}
	if n := count(t, s, "select count(*) from platforms where name = 'Imaginary Console'"); n != 0 {
		t.Error("importer created a platform")
	}
	if n := count(t, s, "select count(*) from game_details where franchise = 'Soulcalibur' and media_item_id = $1", item.ID); n != 1 {
		t.Error("game details missing")
	}

	// Next time the external ID resolves even when the name doesn't match anything.
	game.Editions = []providers.Edition{{Platform: &providers.PlatformRef{ExternalID: &providers.ExternalID{Provider: "igdb_platform", Value: "23"}, Name: "SEGA DC (JP)"},
		ExternalIDs: []providers.ExternalID{{Provider: "igdb_release", Value: "d"}}}}
	if _, err := discover.Import(ctx, s.Deps, "igdb", game, nil); err != nil {
		t.Fatal(err)
	}
	if got := platformOf("d"); got != "dreamcast" {
		t.Errorf("edition d platform = %q", got)
	}
	if n := count(t, s, "select count(*) from editions where media_item_id = $1", item.ID); n != 4 {
		t.Errorf("editions = %d, want 4", n)
	}
}

type countingProvider struct {
	*providertest.Fake
	mu      sync.Mutex
	fetched []string
}

func (c *countingProvider) Fetch(ctx context.Context, kind db.MediaKind, id string) (*providers.Item, error) {
	c.mu.Lock()
	c.fetched = append(c.fetched, id)
	c.mu.Unlock()
	return c.Fake.Fetch(ctx, kind, id)
}

func TestRefreshStaleJob(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	fresh := bookItem("Fresh Book")
	fresh.ExternalIDs = []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL1W"}}
	fresh.Editions = nil
	stale := bookItem("Stale Book")
	stale.ExternalIDs = []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL2W"}}
	stale.Editions = nil
	cp := &countingProvider{Fake: newFake("openlibrary", fresh, stale)}
	s.Deps.Providers = providers.NewRegistry(cp)

	staleItem, err := discover.Import(ctx, s.Deps, "openlibrary", stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discover.Import(ctx, s.Deps, "openlibrary", fresh, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update media_items set metadata_refreshed_at = now() - interval '31 days' where id = $1`, staleItem.ID); err != nil {
		t.Fatal(err)
	}
	testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Manual Book")

	if jobs := discover.PeriodicJobs(s.Deps); len(jobs) != 1 {
		t.Fatalf("periodic jobs = %d", len(jobs))
	}
	w := &discover.RefreshStaleWorker{D: s.Deps}
	if err := w.Work(ctx, &river.Job[discover.RefreshStaleArgs]{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cp.fetched, ",") != "OL2W" {
		t.Errorf("fetched = %v, want only the stale item", cp.fetched)
	}
	got, _ := s.Q.GetMediaItemByID(ctx, staleItem.ID)
	if got.MetadataRefreshedAt == nil || time.Since(*got.MetadataRefreshedAt) > time.Hour {
		t.Errorf("stale item not refreshed: %v", got.MetadataRefreshedAt)
	}

	// Batches are capped at 50.
	for i := range 55 {
		it := bookItem("Bulk")
		it.Editions = nil
		it.ExternalIDs = []providers.ExternalID{{Provider: "openlibrary_work", Value: "OLB" + string(rune('A'+i/26)) + string(rune('A'+i%26)) + "W"}}
		cp.Items[it.ExternalIDs[0].Value] = it
		if _, err := discover.Import(ctx, s.Deps, "openlibrary", it, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `update media_items set metadata_refreshed_at = now() - interval '60 days'`); err != nil {
		t.Fatal(err)
	}
	cp.fetched = nil
	if err := w.Work(ctx, &river.Job[discover.RefreshStaleArgs]{}); err != nil {
		t.Fatal(err)
	}
	if len(cp.fetched) != 50 {
		t.Errorf("batch fetched %d, want 50", len(cp.fetched))
	}
}

func TestDiscoverRequiresLogin(t *testing.T) {
	s := testutil.NewServer(t)
	res := s.Client(t).Get("/discover")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Errorf("anonymous /discover: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = s.Client(t).PostForm("/discover/import", importForm("openlibrary", "book", "openlibrary_work", "OL1W"))
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Errorf("anonymous import: %d", res.StatusCode)
	}
}
