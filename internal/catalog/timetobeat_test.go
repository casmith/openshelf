package catalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestTimeToBeatHeader(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	game := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Hades")
	bare := testutil.CreateItem(t, s.Pool, db.MediaKindGame, "Unknown Game")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into game_time_to_beat (media_item_id, hastily_minutes, normally_minutes, completely_minutes, submissions)
	      values ($1, 62, 149, null, 13)`, game.ID)
	exec(`insert into game_time_to_beat (media_item_id) values ($1)`, bare.ID)
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "765")
	exec(`insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total) values ($1, $2, '1145360', 3840)`, acct.ID, game.ID)

	page := s.LoginAs(t, alice).Get(itemPath(game)).Body
	for _, want := range []string{"Time to beat", "1 h rushed · 2.5 h main", "13 submissions", "IGDB", "played 64 h"} {
		if !strings.Contains(page, want) {
			t.Errorf("alice's page missing %q", want)
		}
	}
	if strings.Contains(page, "completionist") {
		t.Error("missing completionist estimate should be omitted")
	}

	page = s.LoginAs(t, bob).Get(itemPath(game)).Body
	if !strings.Contains(page, "2.5 h main") {
		t.Error("bob should see the estimates")
	}
	if strings.Contains(page, "played 64 h") {
		t.Error("bob sees alice's playtime")
	}

	if page := s.LoginAs(t, alice).Get(itemPath(bare)).Body; strings.Contains(page, "data-time-to-beat") {
		t.Error("game without estimates should render no time-to-beat block")
	}
}
