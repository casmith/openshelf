package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

const (
	achievementsPerRun  = 200
	schemaMaxAge        = 7 * 24 * time.Hour
	privateStatsWarning = "Steam didn't share achievements for this account. Set Game details to Public in your Steam privacy settings to import them."
)

var now = time.Now

// achievementState is cur.Achievements: per appid, what the last successful fetch saw.
type achievementState struct {
	Apps map[string]appState `json:"apps"`
}

type appState struct {
	LastPlayed int64 `json:"lp"`
	FetchedAt  int64 `json:"at"`
	None       bool  `json:"none,omitempty"` // the schema had no achievements
}

type schemaResponse struct {
	Game struct {
		AvailableGameStats struct {
			Achievements []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
				Icon        string `json:"icon"`
				Hidden      int    `json:"hidden"`
			} `json:"achievements"`
		} `json:"availableGameStats"`
	} `json:"game"`
}

type playerResponse struct {
	PlayerStats struct {
		Success      *bool `json:"success"`
		Achievements []struct {
			APIName    string `json:"apiname"`
			Achieved   int    `json:"achieved"`
			UnlockTime int64  `json:"unlocktime"`
		} `json:"achievements"`
	} `json:"playerstats"`
}

var errPrivateStats = errors.New("steam: achievements are private")

// syncAchievements emits achievement schemas and unlocks for owned games whose
// stats may have changed since the last fetch, at most achievementsPerRun per run.
func (a *Adapter) syncAchievements(ctx context.Context, acct db.ConnectedAccount, owned []OwnedApp, cur *Cursor, emit sync.Emitter) error {
	var st achievementState
	if len(cur.Achievements) > 0 {
		_ = json.Unmarshal(cur.Achievements, &st)
	}
	if st.Apps == nil {
		st.Apps = map[string]appState{}
	}
	t := now()
	due := dueApps(owned, st, t)
	defer func() {
		if b, err := json.Marshal(st); err == nil {
			cur.Achievements = b
		}
	}()
	for _, app := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := strconv.FormatInt(app.AppID, 10)
		set, err := a.fetchAchievements(ctx, acct.ExternalAccountID, key)
		switch {
		case errors.Is(err, errPrivateStats):
			emit.Warn(privateStatsWarning)
			return nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		case err != nil:
			emit.Warn(fmt.Sprintf("Couldn't fetch achievements for %s; they'll be retried next sync.", appName(app)))
			continue
		}
		state := appState{LastPlayed: app.LastPlayedUnix, FetchedAt: t.Unix(), None: set == nil}
		if set != nil {
			if err := emit.Achievements(ctx, *set); err != nil {
				return err
			}
		}
		st.Apps[key] = state
	}
	return nil
}

// dueApps picks the games to refresh: never fetched or played since the last
// fetch first (most recently played first), then stale schemas (oldest first).
func dueApps(owned []OwnedApp, st achievementState, t time.Time) []OwnedApp {
	var fresh, stale []OwnedApp
	staleAt := map[int64]int64{}
	for _, app := range owned {
		if !app.HasStats {
			continue
		}
		s, ok := st.Apps[strconv.FormatInt(app.AppID, 10)]
		switch {
		case !ok || (!s.None && s.LastPlayed != app.LastPlayedUnix):
			fresh = append(fresh, app)
		case t.Sub(time.Unix(s.FetchedAt, 0)) >= schemaMaxAge:
			stale = append(stale, app)
			staleAt[app.AppID] = s.FetchedAt
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].LastPlayedUnix > fresh[j].LastPlayedUnix })
	sort.SliceStable(stale, func(i, j int) bool { return staleAt[stale[i].AppID] < staleAt[stale[j].AppID] })
	due := append(fresh, stale...)
	if len(due) > achievementsPerRun {
		due = due[:achievementsPerRun]
	}
	return due
}

// fetchAchievements returns nil when the game's schema has no achievements.
func (a *Adapter) fetchAchievements(ctx context.Context, steamID, appID string) (*sync.AchievementSet, error) {
	var schema schemaResponse
	if err := a.Client.Get(ctx, "ISteamUserStats", "GetSchemaForGame", "v2", url.Values{"appid": {appID}, "l": {"english"}}, &schema); err != nil {
		return nil, err
	}
	defs := schema.Game.AvailableGameStats.Achievements
	if len(defs) == 0 {
		return nil, nil
	}
	var player playerResponse
	err := a.Client.Get(ctx, "ISteamUserStats", "GetPlayerAchievements", "v1", url.Values{"steamid": {steamID}, "appid": {appID}}, &player)
	var se *providers.StatusError
	if errors.As(err, &se) && se.Status == http.StatusForbidden {
		return nil, errPrivateStats
	}
	if err != nil {
		return nil, err
	}
	if ps := player.PlayerStats; ps.Success == nil || !*ps.Success {
		return nil, errPrivateStats
	}
	set := &sync.AchievementSet{
		Key:         appID,
		ExternalIDs: []providers.ExternalID{{Provider: ExternalIDProvider, Value: appID, URL: "https://store.steampowered.com/app/" + appID}},
	}
	for i, d := range defs {
		if d.Name == "" {
			continue
		}
		set.Achievements = append(set.Achievements, sync.Achievement{
			Key: d.Name, Name: d.DisplayName, Description: d.Description,
			IconURL: iconURL(d.Icon), Hidden: d.Hidden != 0, Order: i,
		})
	}
	for _, p := range player.PlayerStats.Achievements {
		if p.Achieved != 1 || p.APIName == "" {
			continue
		}
		u := sync.Unlock{Key: p.APIName}
		if p.UnlockTime > 0 {
			at := time.Unix(p.UnlockTime, 0).UTC()
			u.UnlockedAt = &at
		}
		set.Unlocks = append(set.Unlocks, u)
	}
	return set, nil
}

func appName(app OwnedApp) string {
	if app.Name != "" {
		return app.Name
	}
	return "app " + strconv.FormatInt(app.AppID, 10)
}

const (
	legacyIconPrefix = "https://steamcdn-a.akamaihd.net/steamcommunity/public/images/apps/"
	iconPrefix       = "https://shared.fastly.steamstatic.com/community_assets/images/apps/"
)

// iconURL moves schema icons to Steam's current CDN path. GetSchemaForGame still
// returns the legacy host, which 404s for icons uploaded or updated recently.
func iconURL(u string) string {
	if strings.HasPrefix(u, legacyIconPrefix) {
		return iconPrefix + strings.TrimPrefix(u, legacyIconPrefix)
	}
	return u
}
