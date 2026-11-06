package experiences_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

var finishedOnInput = regexp.MustCompile(`<input id="f-finished_on-new"[^>]*>`)

// unlockAll gives account dated unlocks for item, keyed "name :: description" -> RFC 3339 time.
func unlockAll(t *testing.T, s *testutil.Server, account db.ConnectedAccount, item db.MediaItem, unlocks [][2]string) {
	t.Helper()
	for i, u := range unlocks {
		name, desc, _ := strings.Cut(u[0], " :: ")
		var id int64
		err := s.Pool.QueryRow(ctx,
			`insert into achievements (media_item_id, provider, provider_game_key, provider_achievement_key, name, description)
			 values ($1, 'steam', $2, $3, $4, nullif($5, '')) returning id`,
			item.ID, "app-"+item.PublicID.String(), fmt.Sprintf("a%d", i), name, desc).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, s, `insert into achievement_unlocks (account_id, achievement_id, unlocked_at) values ($1, $2, $3::timestamptz)`, account.ID, id, u[1])
	}
}

func hintSetup(t *testing.T) (*testutil.Server, db.User, db.ConnectedAccount) {
	t.Helper()
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	exec(t, s, `update users set timezone = 'America/Los_Angeles' where id = $1`, alice.ID)
	return s, alice, testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "76561190000000001")
}

func finishForm(t *testing.T, body string) (input, hint string) {
	t.Helper()
	input = finishedOnInput.FindString(body)
	m := regexp.MustCompile(`(?s)<p id="f-finished_on-new-hint"[^>]*>\s*([^<]*?)\s*</p>`).FindStringSubmatch(body)
	if input == "" || m == nil {
		t.Fatal("no Log as finished form")
	}
	return input, m[1]
}

func TestFinishHintFromEndingAchievement(t *testing.T) {
	s, alice, acct := hintSetup(t)
	portal := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Portal")
	unlockAll(t, s, acct, portal, [][2]string{
		{"Lab Rat :: Maneuver inside a Weighted Storage Cube", "2021-11-20T10:00:00Z"},
		{"Heartbreaker :: Complete Portal", "2021-11-27T05:00:00Z"},
		{"Fratricide :: Do whatever it takes to survive", "2022-02-01T10:00:00Z"},
	})
	input, hint := finishForm(t, s.LoginAs(t, alice).Get("/items/"+portal.PublicID.String()).Body)
	if !strings.Contains(input, `value="2021-11-26"`) {
		t.Errorf("input %s", input)
	}
	if hint != "From your Steam achievements: ‘Heartbreaker’ unlocked Nov 26, 2021" {
		t.Errorf("hint %q", hint)
	}
}

func TestFinishHintFromLastUnlock(t *testing.T) {
	s, alice, acct := hintSetup(t)
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Celeste")
	unlockAll(t, s, acct, game, [][2]string{
		{"Thing :: Do a thing", "2024-03-04T03:00:00Z"},
		{"Thinker :: Finish Chapter I.", "2023-01-01T10:00:00Z"},
	})
	input, hint := finishForm(t, s.LoginAs(t, alice).Get("/items/"+game.PublicID.String()).Body)
	if !strings.Contains(input, `value="2024-03-03"`) {
		t.Errorf("input %s", input)
	}
	if hint != "From your last Steam achievement, unlocked Mar 3, 2024 — clear it if that&#39;s not right." {
		t.Errorf("hint %q", hint)
	}
}

func TestFinishHintFromLastPlayed(t *testing.T) {
	s, alice, acct := hintSetup(t)
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Hades")
	exec(t, s, `insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total, last_played_at)
	            values ($1, $2, 'hades', 600, '2024-03-04T03:00:00Z')`, acct.ID, game.ID)
	c := s.LoginAs(t, alice)
	input, hint := finishForm(t, c.Get("/items/"+game.PublicID.String()).Body)
	if !strings.Contains(input, `value="2024-03-03"`) {
		t.Errorf("input %s", input)
	}
	if hint != "From when you last played on Steam (Mar 3, 2024) — clear it if that&#39;s not right." {
		t.Errorf("hint %q", hint)
	}

	unlockAll(t, s, acct, game, [][2]string{{"Thing :: Do a thing", "2023-01-01T10:00:00Z"}})
	if input, _ := finishForm(t, c.Get("/items/"+game.PublicID.String()).Body); !strings.Contains(input, `value="2023-01-01"`) {
		t.Errorf("achievements should take priority: input %s", input)
	}

	bob := testutil.CreateUser(t, s.Q, "bob")
	if input, _ := finishForm(t, s.LoginAs(t, bob).Get("/items/"+game.PublicID.String()).Body); strings.Contains(input, "value=") {
		t.Errorf("bob sees alice's last played: %s", input)
	}
}

func TestFinishHintAbsentWithoutOwnUnlocks(t *testing.T) {
	s, alice, _ := hintSetup(t)
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Portal")
	bob := testutil.CreateUser(t, s.Q, "bob")
	bobAcct := testutil.CreateConnectedAccount(t, s.Pool, bob.ID, "steam", "76561190000000002")
	c := s.LoginAs(t, alice)
	check := func(label string) {
		t.Helper()
		input, hint := finishForm(t, c.Get("/items/"+game.PublicID.String()).Body)
		if strings.Contains(input, "value=") || hint != "Leave blank if you don't remember." {
			t.Errorf("%s: input %s hint %q", label, input, hint)
		}
	}
	check("no unlocks")
	unlockAll(t, s, bobAcct, game, [][2]string{{"Heartbreaker :: Complete Portal", "2021-11-27T05:00:00Z"}})
	check("another user's unlocks")
	if input, _ := finishForm(t, s.LoginAs(t, bob).Get("/items/"+game.PublicID.String()).Body); !strings.Contains(input, `value="2021-11-27"`) {
		t.Errorf("bob (UTC) input %s", input)
	}
}

func TestFinishHintForBooksFromAudiobookshelf(t *testing.T) {
	s, alice, steamAcct := hintSetup(t)
	abs := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "audiobookshelf", "abs-alice")
	progress := func(item db.MediaItem, key string, fraction float64, finished bool, finishedAt, last string) {
		t.Helper()
		exec(t, s, `insert into external_progress (account_id, media_item_id, provider_item_key, fraction, finished, finished_at, last_activity_at)
		            values ($1, $2, $3, $4, $5, nullif($6, '')::timestamptz, nullif($7, '')::timestamptz)`, abs.ID, item.ID, key, fraction, finished, finishedAt, last)
	}
	kings := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "The Way of Kings")
	progress(kings, "li_kings", 1, true, "2026-03-04T03:00:00Z", "2026-05-01T10:00:00Z")
	// Playtime on a book is ignored: books use only the Audiobookshelf basis.
	exec(t, s, `insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total, last_played_at)
	            values ($1, $2, 'odd', 60, '2020-01-01T10:00:00Z')`, steamAcct.ID, kings.ID)
	nearly := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Project Hail Mary")
	progress(nearly, "li_hm", 0.96, false, "", "2026-03-04T03:00:00Z")
	half := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Halfway")
	progress(half, "li_half", 0.5, false, "", "2026-03-04T03:00:00Z")

	c := s.LoginAs(t, alice)
	input, hint := finishForm(t, c.Get("/items/"+kings.PublicID.String()).Body)
	if !strings.Contains(input, `value="2026-03-03"`) || hint != "From Audiobookshelf (finished Mar 3, 2026)" {
		t.Errorf("finished: input %s hint %q", input, hint)
	}
	input, hint = finishForm(t, c.Get("/items/"+nearly.PublicID.String()).Body)
	if !strings.Contains(input, `value="2026-03-03"`) || hint != "From when you last listened on Audiobookshelf (Mar 3, 2026) — clear it if that&#39;s not right." {
		t.Errorf("nearly: input %s hint %q", input, hint)
	}
	if input, _ := finishForm(t, c.Get("/items/"+half.PublicID.String()).Body); strings.Contains(input, "value=") {
		t.Errorf("half-listened book prefilled: %s", input)
	}

	bob := testutil.CreateUser(t, s.Q, "bob")
	if input, _ := finishForm(t, s.LoginAs(t, bob).Get("/items/"+kings.PublicID.String()).Body); strings.Contains(input, "value=") {
		t.Errorf("bob sees alice's progress: %s", input)
	}
}
