package library_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

// absBook stores a book synced from Audiobookshelf the way the runner does.
func absBook(t *testing.T, s *testutil.Server, u db.User, acct db.ConnectedAccount, title string) (db.MediaItem, db.LibraryEntry) {
	t.Helper()
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, title)
	e := testutil.CreateEntry(t, s.Pool, u.ID, item.ID)
	exec(t, s.Pool, `update library_entries set ownership_source = 'owned', played_source = true where id = $1`, e.ID)
	exec(t, s.Pool, `with r as (insert into sync_runs (account_id) values ($1) returning id)
		insert into sync_events (run_id, account_id, user_id, media_item_id, library_entry_id, kind)
		select r.id, $1, $2, $3, $4, 'entry_created' from r`, acct.ID, u.ID, item.ID, e.ID)
	exec(t, s.Pool, `insert into external_progress (account_id, media_item_id, provider_item_key, fraction) values ($1, $2, $3, 0.4)`,
		acct.ID, item.ID, "li_"+item.PublicID.String())
	return item, e
}

func TestBookEntriesSayStarted(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "audiobookshelf", "abs-alice")
	book, entry := absBook(t, s, alice, acct, "Project Hail Mary")
	c := s.LoginAs(t, alice)
	itemURL := "/items/" + book.PublicID.String()

	body := c.Get(itemURL).Body
	page := pageText(body)
	for _, w := range []string{"Owned · from Audiobookshelf", "Started · from Audiobookshelf"} {
		if !strings.Contains(page, w) {
			t.Errorf("item page missing %q", w)
		}
	}
	if !strings.Contains(body, ">Started</button>") || !strings.Contains(body, ">Not started</button>") || strings.Contains(body, ">Played</button>") {
		t.Error("book panel buttons should say Started / Not started")
	}
	if !strings.Contains(body, "imported from Audiobookshelf, so it will also be excluded from future syncs") {
		t.Error("delete confirmation should treat the Audiobookshelf entry as synced")
	}

	table := c.Get("/library/book?view=table").Body
	if !strings.Contains(pageText(table), "Started · from Audiobookshelf") {
		t.Error("table missing book source badge")
	}
	for _, w := range []string{`<option value="played">Started</option>`, `<option value="not_played">Not started</option>`} {
		if !strings.Contains(table, w) {
			t.Errorf("book filter missing %s", w)
		}
	}
	if games := c.Get("/library/game").Body; !strings.Contains(games, `<option value="played">Played</option>`) {
		t.Error("game filter should keep Played")
	}

	c.PostForm("/library/"+entry.PublicID.String()+"/played", url.Values{"played": {"false"}})
	if page := pageText(c.Get(itemURL).Body); !strings.Contains(page, "Not started (you) · Audiobookshelf says started") {
		t.Errorf("override label missing: %s", page)
	}

	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Portal")
	testutil.CreateEntry(t, s.Pool, alice.ID, game.ID)
	if body := c.Get("/items/" + game.PublicID.String()).Body; !strings.Contains(body, ">Played</button>") {
		t.Error("game panel should keep Played")
	}
}
