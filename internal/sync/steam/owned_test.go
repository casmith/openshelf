package steam

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/synctest"
)

const testSteamID = "76561197960287930"

func acctFor(id string) db.ConnectedAccount {
	return db.ConnectedAccount{Provider: Provider, ExternalAccountID: id}
}

func TestSyncOwnedEmitsGames(t *testing.T) {
	fs := NewFakeSteam(t)
	fi, ip := NewFakeIGDB(t)
	a := fs.Adapter(providers.NewRegistry(ip))
	rec := &synctest.Recorder{}
	owned, err := a.syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Warnings) != 0 || len(rec.Games) != 3 || len(owned) != 3 {
		t.Fatalf("warnings %v, %d games, %d owned", rec.Warnings, len(rec.Games), len(owned))
	}
	if fi.Count("/v4/external_games") != 1 {
		t.Errorf("%d external_games queries, want 1 batch", fi.Count("/v4/external_games"))
	}

	p2 := rec.Games[0]
	if p2.Key != "620" || p2.Title != "Portal 2" || *p2.PlaytimeMinutes != 1800 || *p2.Playtime2Weeks != 95 {
		t.Errorf("portal 2: %+v", p2)
	}
	if p2.LastPlayedAt == nil || !p2.LastPlayedAt.Equal(time.Unix(1762300800, 0)) {
		t.Errorf("last played %v", p2.LastPlayedAt)
	}
	if p2.CoverURL != "https://shared.steamstatic.com/store_item_assets/steam/apps/620/library_600x900.jpg" {
		t.Errorf("cover %s", p2.CoverURL)
	}
	wantIDs := []providers.ExternalID{
		{Provider: "steam_app", Value: "620", URL: "https://store.steampowered.com/app/620"},
		{Provider: "igdb_game", Value: "72"},
	}
	if len(p2.ExternalIDs) != 2 || p2.ExternalIDs[0] != wantIDs[0] || p2.ExternalIDs[1] != wantIDs[1] {
		t.Errorf("external IDs %+v", p2.ExternalIDs)
	}

	portal := rec.Games[1]
	if *portal.PlaytimeMinutes != 0 || *portal.Playtime2Weeks != 0 || portal.LastPlayedAt != nil {
		t.Errorf("portal: %+v", portal)
	}
	indie := rec.Games[2]
	if len(indie.ExternalIDs) != 1 {
		t.Errorf("unmatched game has hints: %+v", indie.ExternalIDs)
	}
	if owned[0].AppID != 620 || owned[0].PlaytimeMinutes != 1800 || owned[0].LastPlayedUnix != 1762300800 {
		t.Errorf("owned %+v", owned[0])
	}
}

func TestSyncOwnedWithoutIGDB(t *testing.T) {
	fs := NewFakeSteam(t)
	fi, _ := NewFakeIGDB(t)
	unconfigured := providers.NewRegistry()
	for _, reg := range []*providers.Registry{nil, unconfigured} {
		rec := &synctest.Recorder{}
		if _, err := fs.Adapter(reg).syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, rec); err != nil {
			t.Fatal(err)
		}
		if len(rec.Games) != 3 || len(rec.Games[0].ExternalIDs) != 1 {
			t.Fatalf("games %+v", rec.Games)
		}
	}
	if fi.Count("/v4/external_games") != 0 {
		t.Error("looked up IGDB without it configured")
	}
}

func TestSyncOwnedIGDBFailureImportsNothing(t *testing.T) {
	fs := NewFakeSteam(t)
	fi, ip := NewFakeIGDB(t)
	fi.Fail = true
	rec := &synctest.Recorder{}
	owned, err := fs.Adapter(providers.NewRegistry(ip)).syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Games) != 0 || len(rec.Warnings) != 1 || len(owned) != 3 {
		t.Fatalf("games %d warnings %v owned %d", len(rec.Games), rec.Warnings, len(owned))
	}
}

func TestSyncOwnedPrivacy(t *testing.T) {
	fs := NewFakeSteam(t)
	a := fs.Adapter(nil)

	fs.Players["76561197960287931"] = PlayerSummary{SteamID: "76561197960287931", CommunityVisibilityState: 1}
	fs.Players["76561197960287932"] = PlayerSummary{SteamID: "76561197960287932", CommunityVisibilityState: 3}
	fs.Owned["76561197960287932"] = `{"response":{}}`

	for id, want := range map[string]string{
		"76561197960287931": "profile is private",
		"76561197960287932": "Game details to Public",
		"76561197960287999": "didn't return this profile",
	} {
		rec := &synctest.Recorder{}
		owned, err := a.syncOwned(context.Background(), acctFor(id), &Cursor{}, rec)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(rec.Games) != 0 || owned != nil || len(rec.Warnings) != 1 || !strings.Contains(rec.Warnings[0], want) {
			t.Errorf("%s: games %d owned %v warnings %v", id, len(rec.Games), owned, rec.Warnings)
		}
	}

	fs.Owned[testSteamID] = `{"response":{"game_count":0}}`
	rec := &synctest.Recorder{}
	if _, err := a.syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, rec); err != nil || len(rec.Warnings) != 0 {
		t.Errorf("public empty library: %v %v", err, rec.Warnings)
	}
}

func TestSyncOwnedForbidden(t *testing.T) {
	fs := NewFakeSteam(t)
	a := fs.Adapter(nil)
	a.Client.Key = "revoked"
	_, err := a.syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, &synctest.Recorder{})
	var se *providers.StatusError
	if !errors.As(err, &se) || se.Status != 403 || !strings.Contains(err.Error(), "OPENSHELF_STEAM_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncOwnedPropagatesEmitErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := NewFakeSteam(t).Adapter(nil).syncOwned(context.Background(), acctFor(testSteamID), &Cursor{}, &synctest.Recorder{Err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	fs := NewFakeSteam(t)
	_, ip := NewFakeIGDB(t)
	a := fs.Adapter(providers.NewRegistry(ip))
	ctx := context.Background()
	steamOnly := providers.ExternalID{Provider: "steam_app", Value: "620"}

	res, err := a.Resolve(ctx, gameWith(steamOnly, providers.ExternalID{Provider: "igdb_game", Value: "72"}))
	if err != nil || res == nil || res.Provider != "igdb" || res.Item.Title != "Portal 2" {
		t.Fatalf("resolve hinted: %+v %v", res, err)
	}
	for _, g := range []struct {
		name string
		a    *Adapter
		ids  []providers.ExternalID
	}{
		{"no hint", a, []providers.ExternalID{steamOnly}},
		{"deleted on IGDB", a, []providers.ExternalID{steamOnly, {Provider: "igdb_game", Value: "71"}}},
		{"igdb unconfigured", fs.Adapter(nil), []providers.ExternalID{steamOnly, {Provider: "igdb_game", Value: "72"}}},
	} {
		if res, err := g.a.Resolve(ctx, gameWith(g.ids...)); err != nil || res != nil {
			t.Errorf("%s: %+v %v", g.name, res, err)
		}
	}
}

func gameWith(ids ...providers.ExternalID) sync.OwnedGame {
	return sync.OwnedGame{Key: "620", Title: "Portal 2", ExternalIDs: ids}
}
