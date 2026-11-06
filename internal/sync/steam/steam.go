// Package steam is the Steam sync adapter. S20 owns linking (openid.go,
// handlers.go) and owned games (owned.go); S21 owns achievements.go.
package steam

import (
	"context"
	"encoding/json"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

const (
	Provider = "steam"
	// ExternalIDProvider is the external_ids namespace for Steam app IDs.
	ExternalIDProvider = "steam_app"
)

type Adapter struct {
	Client         *Client
	OpenIDEndpoint string
	// Metadata is the provider registry, for resolving Steam apps to IGDB games. Set by internal/app.
	Metadata *providers.Registry
}

var _ sync.Adapter = (*Adapter)(nil)

func New(cfg config.Config) *Adapter {
	return &Adapter{
		Client:         NewClient(cfg.SteamAPIKey),
		OpenIDEndpoint: "https://steamcommunity.com/openid/login",
	}
}

func (a *Adapter) Provider() string { return Provider }

func (a *Adapter) Configured() bool { return a.Client.Key != "" }

func (a *Adapter) Capabilities() sync.Capabilities {
	return sync.Capabilities{
		DisplayName:  "Steam",
		LinkPath:     "/connections/steam/link",
		OwnedGames:   true,
		Playtime:     true,
		Achievements: true,
	}
}

// Cursor is the adapter's stored state (JSON). Each half belongs to one file.
type Cursor struct {
	Owned        json.RawMessage `json:"owned,omitempty"`        // owned.go
	Achievements json.RawMessage `json:"achievements,omitempty"` // achievements.go
}

// OwnedApp is what syncOwned reports back so syncAchievements can pick which games to refresh.
type OwnedApp struct {
	AppID           int64
	Name            string
	PlaytimeMinutes int
	LastPlayedUnix  int64
	HasStats        bool // has_community_visible_stats
}

func (a *Adapter) Sync(ctx context.Context, acct db.ConnectedAccount, cursor []byte, emit sync.Emitter) ([]byte, error) {
	var cur Cursor
	if len(cursor) > 0 {
		if err := json.Unmarshal(cursor, &cur); err != nil {
			cur = Cursor{}
		}
	}
	owned, err := a.syncOwned(ctx, acct, &cur, emit)
	if err != nil {
		return nil, err
	}
	if err := a.syncAchievements(ctx, acct, owned, &cur, emit); err != nil {
		return nil, err
	}
	return json.Marshal(cur)
}
