package steam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

const (
	coverURLFormat   = "https://shared.steamstatic.com/store_item_assets/steam/apps/%d/library_600x900.jpg"
	storeURLFormat   = "https://store.steampowered.com/app/%s"
	visibilityPublic = 3

	warnPrivateProfile = "Your Steam profile is private, so nothing was imported. Set your Steam profile and game details to Public, then sync again."
	warnPrivateGames   = "Steam didn't share your games list, so nothing was imported. Set Game details to Public in your Steam privacy settings, then sync again."
	warnNoProfile      = "Steam didn't return this profile, so nothing was imported. Check that the account still exists."
)

// steamAppLookup is implemented by the IGDB provider.
type steamAppLookup interface {
	GamesBySteamApp(ctx context.Context, appIDs []string) (map[string]string, error)
}

type ownedGamesResponse struct {
	Response struct {
		GameCount *int `json:"game_count"`
		Games     []struct {
			AppID           int64  `json:"appid"`
			Name            string `json:"name"`
			PlaytimeForever int    `json:"playtime_forever"`
			Playtime2Weeks  *int   `json:"playtime_2weeks"`
			RtimeLastPlayed int64  `json:"rtime_last_played"`
			HasStats        bool   `json:"has_community_visible_stats"`
		} `json:"games"`
	} `json:"response"`
}

// PlayerSummary is the profile part of ISteamUser/GetPlayerSummaries.
type PlayerSummary struct {
	SteamID                  string `json:"steamid"`
	PersonaName              string `json:"personaname"`
	ProfileURL               string `json:"profileurl"`
	AvatarMedium             string `json:"avatarmedium"`
	CommunityVisibilityState int    `json:"communityvisibilitystate"`
}

// PlayerSummary returns nil, nil when Steam doesn't know the SteamID.
func (a *Adapter) PlayerSummary(ctx context.Context, steamID string) (*PlayerSummary, error) {
	var out struct {
		Response struct {
			Players []PlayerSummary `json:"players"`
		} `json:"response"`
	}
	if err := a.Client.Get(ctx, "ISteamUser", "GetPlayerSummaries", "v2", url.Values{"steamids": {steamID}}, &out); err != nil {
		return nil, apiError("loading the Steam profile", err)
	}
	for _, p := range out.Response.Players {
		if p.SteamID == steamID {
			return &p, nil
		}
	}
	return nil, nil
}

func (a *Adapter) syncOwned(ctx context.Context, acct db.ConnectedAccount, cur *Cursor, emit sync.Emitter) ([]OwnedApp, error) {
	steamID := acct.ExternalAccountID
	profile, err := a.PlayerSummary(ctx, steamID)
	if err != nil {
		return nil, err
	}
	switch {
	case profile == nil:
		emit.Warn(warnNoProfile)
		return nil, nil
	case profile.CommunityVisibilityState != visibilityPublic:
		emit.Warn(warnPrivateProfile)
		return nil, nil
	}

	var res ownedGamesResponse
	params := url.Values{"steamid": {steamID}, "include_appinfo": {"1"}, "include_played_free_games": {"1"}}
	if err := a.Client.Get(ctx, "IPlayerService", "GetOwnedGames", "v1", params, &res); err != nil {
		return nil, apiError("loading owned games", err)
	}
	if res.Response.GameCount == nil {
		emit.Warn(warnPrivateGames)
		return nil, nil
	}

	owned := make([]OwnedApp, 0, len(res.Response.Games))
	keys := make([]string, 0, len(res.Response.Games))
	for _, g := range res.Response.Games {
		if g.AppID <= 0 {
			continue
		}
		owned = append(owned, OwnedApp{
			AppID: g.AppID, Name: g.Name, PlaytimeMinutes: g.PlaytimeForever,
			LastPlayedUnix: g.RtimeLastPlayed, HasStats: g.HasStats,
		})
		keys = append(keys, strconv.FormatInt(g.AppID, 10))
	}

	igdbIDs, err := a.lookupIGDB(ctx, keys)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Importing now would create manual items that later IGDB matches can't replace.
		emit.Warn("Couldn't look up game matches on IGDB, so games weren't imported this time. They will be retried on the next sync.")
		return owned, nil
	}

	for _, g := range res.Response.Games {
		if g.AppID <= 0 {
			continue
		}
		key := strconv.FormatInt(g.AppID, 10)
		og := sync.OwnedItem{
			Key:         key,
			Title:       g.Name,
			ExternalIDs: []providers.ExternalID{{Provider: ExternalIDProvider, Value: key, URL: fmt.Sprintf(storeURLFormat, key)}},
			CoverURL:    fmt.Sprintf(coverURLFormat, g.AppID),
		}
		if id := igdbIDs[key]; id != "" {
			og.ExternalIDs = append(og.ExternalIDs, providers.ExternalID{Provider: "igdb_game", Value: id})
		}
		total := g.PlaytimeForever
		og.PlaytimeMinutes = &total
		// Steam omits playtime_2weeks when the game wasn't played in the last two weeks.
		twoWeeks := 0
		if g.Playtime2Weeks != nil {
			twoWeeks = *g.Playtime2Weeks
		}
		og.Playtime2Weeks = &twoWeeks
		if g.RtimeLastPlayed > 0 {
			t := time.Unix(g.RtimeLastPlayed, 0).UTC()
			og.LastPlayedAt = &t
		}
		if err := emit.OwnedItem(ctx, og); err != nil {
			return nil, err
		}
	}
	return owned, nil
}

func (a *Adapter) igdb() (providers.Provider, steamAppLookup, bool) {
	if a.Metadata == nil {
		return nil, nil, false
	}
	p, ok := a.Metadata.Get("igdb")
	if !ok || !p.Configured() {
		return nil, nil, false
	}
	l, ok := p.(steamAppLookup)
	return p, l, ok
}

func (a *Adapter) lookupIGDB(ctx context.Context, appIDs []string) (map[string]string, error) {
	_, l, ok := a.igdb()
	if !ok || len(appIDs) == 0 {
		return nil, nil
	}
	return l.GamesBySteamApp(ctx, appIDs)
}

func apiError(doing string, err error) error {
	var se *providers.StatusError
	if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
		return fmt.Errorf("steam refused the request while %s (HTTP %d); an administrator should check OPENSHELF_STEAM_API_KEY: %w", doing, se.Status, err)
	}
	return fmt.Errorf("steam: %s: %w", doing, err)
}
