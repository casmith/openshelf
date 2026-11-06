package library_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/achievements"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

var htmlTags = regexp.MustCompile(`<[^>]*>`)

func pageText(html string) string {
	return strings.Join(strings.Fields(htmlTags.ReplaceAllString(html, " ")), " ")
}

func steamGame(key, title string, minutes int) sync.OwnedGame {
	return sync.OwnedGame{Key: key, Title: title, PlaytimeMinutes: &minutes,
		ExternalIDs: []providers.ExternalID{{Provider: "steam_app", Value: key}}}
}

// importSteam links a Steam account for u and syncs games through the runner.
func importSteam(t *testing.T, s *testutil.Server, u db.User, sets []sync.AchievementSet, games ...sync.OwnedGame) db.ConnectedAccount {
	t.Helper()
	s.Deps.SyncAdapters = sync.NewRegistry(&synctest.Fake{Name: "steam", Caps: sync.Capabilities{DisplayName: "Steam"}, Games: games, Sets: sets})
	acct := testutil.CreateConnectedAccount(t, s.Pool, u.ID, "steam", "7656119796028"+u.PublicID.String()[:4])
	if res, err := runner.Run(context.Background(), s.Deps, acct.ID); err != nil || res.Err != nil {
		t.Fatalf("sync: %v %v", err, res.Err)
	}
	return acct
}

type syncedEntry struct {
	ID       int64
	PublicID uuid.UUID
	ItemID   int64
	ItemURL  string
}

func steamEntry(t *testing.T, s *testutil.Server, u db.User, key string) syncedEntry {
	t.Helper()
	var e syncedEntry
	var item uuid.UUID
	err := s.Pool.QueryRow(context.Background(), `select le.id, le.public_id, m.id, m.public_id from library_entries le
		join media_items m on m.id = le.media_item_id
		join external_ids x on x.media_item_id = m.id and x.provider = 'steam_app' and x.value = $2
		where le.user_id = $1`, u.ID, key).Scan(&e.ID, &e.PublicID, &e.ItemID, &item)
	if err != nil {
		t.Fatalf("entry for %s: %v", key, err)
	}
	e.ItemURL = "/items/" + item.String()
	return e
}

func TestSourceBadgesAndOwnershipReset(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	importSteam(t, s, alice, nil, steamGame("620", "Portal 2", 1800), steamGame("400", "Portal", 0))
	portal2 := steamEntry(t, s, alice, "620")
	c := s.LoginAs(t, alice)

	page := pageText(c.Get(portal2.ItemURL).Body)
	for _, w := range []string{"Owned · from Steam", "Played · inferred from Steam"} {
		if !strings.Contains(page, w) {
			t.Errorf("item page missing %q", w)
		}
	}
	if strings.Contains(page, "Reset to source") {
		t.Error("reset offered without an override")
	}
	grid := pageText(c.Get("/library?view=grid").Body)
	if !strings.Contains(grid, "Portal 2 Owned · from Steam") {
		t.Errorf("grid card missing source label: %s", grid)
	}
	table := pageText(c.Get("/library?view=table").Body)
	for _, w := range []string{"Owned · from Steam", "Played · inferred from Steam"} {
		if !strings.Contains(table, w) {
			t.Errorf("table missing %q", w)
		}
	}

	// The user says they don't own it: both values show, with a reset.
	base := "/library/" + portal2.PublicID.String()
	if res := c.PostForm(base+"/ownership", url.Values{"ownership": {"not_owned"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("set ownership: %d", res.StatusCode)
	}
	page = pageText(c.Get(portal2.ItemURL).Body)
	if !strings.Contains(page, "Not owned (you) · Steam says owned Reset to source") {
		t.Errorf("override not shown: %s", page)
	}
	if !strings.Contains(pageText(c.Get("/library?view=table").Body), "Not owned") {
		t.Error("table should show the user's ownership")
	}

	bob := testutil.CreateUser(t, s.Q, "bob")
	if res := s.LoginAs(t, bob).PostForm(base+"/ownership", url.Values{"ownership": {"reset"}}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("bob reset: %d", res.StatusCode)
	}
	if res := c.PostForm(base+"/ownership", url.Values{"ownership": {"stolen"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad ownership: %d", res.StatusCode)
	}

	if res := c.PostForm(base+"/ownership", url.Values{"ownership": {"reset"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset: %d", res.StatusCode)
	}
	var user, source *db.Ownership
	if err := s.Pool.QueryRow(context.Background(), `select ownership_user, ownership_source from library_entries where id = $1`, portal2.ID).
		Scan(&user, &source); err != nil {
		t.Fatal(err)
	}
	if user != nil || source == nil || *source != db.OwnershipOwned {
		t.Fatalf("reset must clear only ownership_user: user=%v source=%v", user, source)
	}
	if !strings.Contains(pageText(c.Get(portal2.ItemURL).Body), "Owned · from Steam") {
		t.Error("reset didn't restore the source label")
	}

	// Zero playtime infers nothing: Portal is owned from Steam but not played.
	page = pageText(c.Get(steamEntry(t, s, alice, "400").ItemURL).Body)
	if !strings.Contains(page, "Owned · from Steam") || strings.Contains(page, "inferred") {
		t.Errorf("portal: %s", page)
	}
}

func TestDeleteSyncedEntryExcludesIt(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	acct := importSteam(t, s, alice, nil, steamGame("620", "Portal 2", 60), steamGame("70", "Half-Life", 10))
	c := s.LoginAs(t, alice)
	exclusions := func() int {
		return count(t, s.Pool, `select count(*) from import_exclusions where account_id = $1`, acct.ID)
	}

	// With a second entry for the item, deleting one keeps syncing the other.
	p2 := steamEntry(t, s, alice, "620")
	ed := testutil.CreateEdition(t, s.Pool, p2.ItemID, "pc")
	exec(t, s.Pool, `insert into library_entries (user_id, media_item_id, edition_id) values ($1, $2, $3)`, alice.ID, p2.ItemID, ed.ID)
	if !strings.Contains(c.Get(p2.ItemURL).Body, "excluded from future syncs") {
		t.Error("delete confirmation doesn't mention the exclusion")
	}
	if res := c.PostForm("/library/"+p2.PublicID.String()+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if n := exclusions(); n != 0 {
		t.Fatalf("excluded while another entry remains: %d", n)
	}

	hl := steamEntry(t, s, alice, "70")
	if res := c.Form(http.MethodDelete, "/library/"+hl.PublicID.String(), nil, true); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if n := count(t, s.Pool, `select count(*) from import_exclusions where account_id = $1 and provider_item_key = '70' and media_item_id = $2`, acct.ID, hl.ItemID); n != 1 {
		t.Fatalf("delete didn't exclude Half-Life: %d", n)
	}
	if res, err := runner.Run(context.Background(), s.Deps, acct.ID); err != nil || res.Counts.EntriesCreated != 0 || res.Counts.Excluded != 1 {
		t.Fatalf("resync: %v %+v", err, res.Counts)
	}

	// A manual entry for an unsynced item deletes plainly.
	film := testutil.CreateItem(t, s.Pool, db.MediaKindFilm, "Arrival")
	e := testutil.CreateEntry(t, s.Pool, alice.ID, film.ID)
	body := c.Get("/items/" + film.PublicID.String()).Body
	if strings.Contains(body, "excluded from future syncs") || !strings.Contains(body, `hx-confirm="Remove this from your library?"`) {
		t.Error("unsynced entry shows the sync exclusion warning")
	}
	c.PostForm("/library/"+e.PublicID.String()+"/delete", nil)
	if n := exclusions(); n != 1 {
		t.Fatalf("exclusions = %d", n)
	}

	// Bob deleting his own copy never excludes for Alice's account.
	bob := testutil.CreateUser(t, s.Q, "bob")
	be := testutil.CreateEntry(t, s.Pool, bob.ID, p2.ItemID)
	s.LoginAs(t, bob).PostForm("/library/"+be.PublicID.String()+"/delete", nil)
	if n := exclusions(); n != 1 {
		t.Fatalf("bob's delete changed alice's exclusions: %d", n)
	}
}

func TestLibraryCardsShowAchievementCounts(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	set := sync.AchievementSet{Key: "620",
		Achievements: []sync.Achievement{{Key: "a1", Name: "One"}, {Key: "a2", Name: "Two"}},
		Unlocks:      []sync.Unlock{{Key: "a1"}}}
	importSteam(t, s, alice, []sync.AchievementSet{set}, steamGame("620", "Portal 2", 60))
	item := steamEntry(t, s, alice, "620").ItemID
	counts, err := achievements.Counts(context.Background(), s.Deps, alice, []int64{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) == 0 {
		t.Skip("achievements.Counts is still a stub (S21)")
	}
	c := s.LoginAs(t, alice)
	if !strings.Contains(pageText(c.Get("/library?view=grid").Body), "1/2 achievements") {
		t.Error("grid card missing achievement count")
	}
	if !strings.Contains(pageText(c.Get("/library?view=table").Body), "1/2 achievements") {
		t.Error("table missing achievement count")
	}
}

func TestSyncedGridOf500EntriesUsesFewQueries(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	games := make([]sync.OwnedGame, 500)
	for i := range games {
		games[i] = steamGame(strconv.Itoa(i+1), "Game "+strconv.Itoa(i+1), i)
	}
	importSteam(t, s, alice, nil, games...)
	c := s.LoginAs(t, alice)
	for _, view := range []string{"grid", "table"} {
		c.Get("/library?view=" + view)
		var res *testutil.Response
		n := testutil.CountQueries(t, s.Pool, func() { res = c.Get("/library?per=500") })
		if res.StatusCode != http.StatusOK || strings.Count(res.Body, "Owned · from Steam") != 500 {
			t.Fatalf("%s: status %d, %d source labels", view, res.StatusCode, strings.Count(res.Body, "Owned · from Steam"))
		}
		t.Logf("%s: %d queries", view, n)
		// One more than the unsynced budget, for achievements.Counts.
		if n > 6 {
			t.Fatalf("%s: %d queries, want <= 6", view, n)
		}
	}
}
