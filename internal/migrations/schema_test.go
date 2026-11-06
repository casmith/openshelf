package migrations_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestConstraints(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Shenmue")
	dc := testutil.CreateEdition(t, s.Pool, item.ID, "dreamcast")

	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s: expected constraint violation", name)
		}
	}
	mustFail("external id without subject", `insert into external_ids (provider, value) values ('igdb_game', '1')`)
	mustFail("external id with two subjects",
		`insert into external_ids (provider, value, media_item_id, edition_id) values ('igdb_game', '1', $1, $2)`, item.ID, dc.ID)
	mustFail("review without rating or body", `insert into reviews (user_id, media_item_id) values ($1, $2)`, u.ID, item.ID)
	mustFail("rating out of range", `insert into reviews (user_id, media_item_id, rating) values ($1, $2, 101)`, u.ID, item.ID)
	mustFail("bad username", `insert into users (username, display_name, password_hash) values ('Bad Name', 'x', 'x')`)

	testutil.CreateEntry(t, s.Pool, u.ID, item.ID)
	mustFail("duplicate edition-less entry", `insert into library_entries (user_id, media_item_id) values ($1, $2)`, u.ID, item.ID)
	if _, err := s.Pool.Exec(ctx, `insert into library_entries (user_id, media_item_id, edition_id) values ($1, $2, $3)`, u.ID, item.ID, dc.ID); err != nil {
		t.Errorf("entry for a specific edition should be allowed: %v", err)
	}
}

func TestSyncConstraints(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Portal 2")
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "765")

	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s: expected constraint violation", name)
		}
	}
	mustFail("same external account for two users",
		`insert into connected_accounts (user_id, provider, external_account_id) values ($1, 'steam', '765')`, bob.ID)
	mustFail("bad status", `update connected_accounts set status = 'paused' where id = $1`, acct.ID)
	if _, err := s.Pool.Exec(ctx, `insert into import_exclusions (account_id, provider_item_key) values ($1, '620')`, acct.ID); err != nil {
		t.Fatal(err)
	}
	mustFail("duplicate exclusion", `insert into import_exclusions (account_id, provider_item_key) values ($1, '620')`, acct.ID)
	if _, err := s.Pool.Exec(ctx, `insert into achievements (media_item_id, provider, provider_game_key, provider_achievement_key, name)
		values ($1, 'steam', '620', 'a1', 'x')`, item.ID); err != nil {
		t.Fatal(err)
	}
	mustFail("duplicate achievement", `insert into achievements (provider, provider_game_key, provider_achievement_key, name)
		values ('steam', '620', 'a1', 'y')`)
	if _, err := s.Pool.Exec(ctx, `insert into achievement_unlocks (account_id, achievement_id)
		select $1, id from achievements`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into achievement_unlocks (account_id, achievement_id, mode)
		select $1, id, 'hardcore' from achievements`, acct.ID); err != nil {
		t.Errorf("a second mode should be allowed: %v", err)
	}
	mustFail("duplicate unlock", `insert into achievement_unlocks (account_id, achievement_id) select $1, id from achievements`, acct.ID)

	if _, err := s.Pool.Exec(ctx, `delete from connected_accounts where id = $1`, acct.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select (select count(*) from import_exclusions) + (select count(*) from achievement_unlocks)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("account delete should cascade source rows: %d %v", n, err)
	}
}

func TestPlatformsSeeded(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	for name, slug := range map[string]string{"Dreamcast": "dreamcast", "mega drive": "genesis", "PS2": "ps2", "Famicom": "nes", "pc": "pc"} {
		p, err := s.Q.FindPlatformByName(ctx, name)
		if err != nil || p.Slug != slug {
			t.Errorf("FindPlatformByName(%q) = %q, %v; want %q", name, p.Slug, err, slug)
		}
	}
}

func TestCatalogSearchRanksExactTitleFirst(t *testing.T) {
	s := testutil.NewServer(t)
	testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune Messiah")
	testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Dune")
	book := db.MediaKindBook
	rows, err := s.Q.CatalogSearch(context.Background(), db.CatalogSearchParams{Query: "dune", Kind: &book, Lim: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Title != "Dune" {
		t.Fatalf("got %d rows, first %q", len(rows), rows[0].Title)
	}
}

func TestItemPage(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Jet Set Radio")
	testutil.CreateEdition(t, s.Pool, item.ID, "dreamcast")
	c := s.LoginAs(t, u)

	res := c.Get("/items/" + item.PublicID.String())
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "Jet Set Radio") || !strings.Contains(res.Body, "Sega Dreamcast") {
		t.Fatalf("item page: status %d", res.StatusCode)
	}
	if res := c.Get("/items/not-a-uuid"); res.StatusCode != http.StatusNotFound {
		t.Errorf("bad id: status %d", res.StatusCode)
	}
	if res := c.Get("/items/00000000-0000-7000-8000-000000000000"); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing item: status %d", res.StatusCode)
	}
}
