// Package sync defines the account sync contract: adapters observe a connected
// account and emit observations; internal/sync/runner applies them (ADR 0002:
// only *_source columns and source-scoped tables are ever written).
//
// Importers that also need the standard library package should alias this one.
package sync

import (
	"context"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

// Unlock modes. RetroAchievements adds hardcore/softcore.
const (
	ModeStandard = "standard"
	ModeHardcore = "hardcore"
	ModeSoftcore = "softcore"
)

type Adapter interface {
	Provider() string // "steam"; matches connected_accounts.provider
	Configured() bool // false when the admin hasn't set the adapter's credentials
	Capabilities() Capabilities
	// Sync observes acct and reports through emit. It returns the cursor to store for
	// the next run; on error the old cursor is kept. Emit errors must be returned as-is.
	Sync(ctx context.Context, acct db.ConnectedAccount, cursor []byte, emit Emitter) ([]byte, error)
}

// Capabilities describes an adapter for the connections page and the runner.
type Capabilities struct {
	DisplayName  string // "Steam"
	LinkPath     string // where "Link account" points, e.g. /connections/steam/link
	OwnedGames   bool
	Playtime     bool
	Achievements bool
}

// Resolver is optionally implemented by an adapter to map an owned game that has
// no external_ids match to a metadata provider item (e.g. Steam app -> IGDB game).
// Return nil, nil when there is no match; the runner then creates a manual item.
// Errors skip the game for this run with a warning rather than creating a duplicate.
type Resolver interface {
	Resolve(ctx context.Context, g OwnedGame) (*Resolution, error)
}

// Resolution is a provider item to import via discover.Import (Provider is its registry name).
type Resolution struct {
	Provider string
	Item     *providers.Item
}

// Emitter receives observations. Each call is applied in its own transaction.
type Emitter interface {
	OwnedGame(ctx context.Context, g OwnedGame) error
	Achievements(ctx context.Context, s AchievementSet) error
	// Warn records a partial result (private profile, unavailable endpoint) on the run.
	Warn(msg string)
}

// OwnedGame is a game the account owns. Key is the provider item key (Steam appid);
// ExternalIDs carry the same identity in external_ids form (steam_app).
type OwnedGame struct {
	Key             string
	Title           string
	ExternalIDs     []providers.ExternalID
	PlaytimeMinutes *int // nil when the provider didn't report playtime
	Playtime2Weeks  *int
	LastPlayedAt    *time.Time
	CoverURL        string
}

// AchievementSet is the full schema and the account's unlocks for one game. Key
// matches OwnedGame.Key; ExternalIDs are a fallback when the game wasn't emitted.
type AchievementSet struct {
	Key          string
	ExternalIDs  []providers.ExternalID
	Achievements []Achievement
	Unlocks      []Unlock
}

type Achievement struct {
	Key         string
	Name        string
	Description string
	IconURL     string
	Hidden      bool
	Order       int
}

type Unlock struct {
	Key        string // Achievement.Key
	UnlockedAt *time.Time
	Mode       string // "" means ModeStandard
}

type Registry struct {
	list []Adapter
}

func NewRegistry(as ...Adapter) *Registry { return &Registry{list: as} }

func (r *Registry) All() []Adapter {
	if r == nil {
		return nil
	}
	return r.list
}

func (r *Registry) Get(provider string) (Adapter, bool) {
	for _, a := range r.All() {
		if a.Provider() == provider {
			return a, true
		}
	}
	return nil, false
}

// sync_events.kind values.
const (
	EventEntryCreated           = "entry_created"
	EventNeedsReview            = "needs_review" // a manual item was created for an unmatched game
	EventOwnershipSourceChanged = "ownership_source_changed"
	EventPlayedSourceChanged    = "played_source_changed"
	EventPlaytimeChanged        = "playtime_changed"
	EventAchievementUnlocked    = "achievement_unlocked"
	EventOverrideRespected      = "override_respected" // source changed elsewhere; the user's value was kept
	EventExcludedSkipped        = "excluded_skipped"
)

// sync_events.field values.
const (
	FieldOwnership = "ownership"
	FieldPlayed    = "played"
	FieldPlaytime  = "playtime_minutes"
)
