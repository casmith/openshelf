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
	Audiobooks   bool
	Progress     bool
}

// Resolver is optionally implemented by an adapter to map an owned item that has
// no external_ids match to a metadata provider item (e.g. Steam app -> IGDB game).
// Return nil, nil when there is no match; the runner then creates a manual item.
// Errors skip the game for this run with a warning rather than creating a duplicate.
type Resolver interface {
	Resolve(ctx context.Context, g OwnedItem) (*Resolution, error)
}

// Resolution is a provider item to import via discover.Import (Provider is its registry name).
type Resolution struct {
	Provider string
	Item     *providers.Item
}

// Emitter receives observations. Each call is applied in its own transaction.
type Emitter interface {
	OwnedItem(ctx context.Context, g OwnedItem) error
	Achievements(ctx context.Context, s AchievementSet) error
	// Warn records a partial result (private profile, unavailable endpoint) on the run.
	Warn(msg string)
}

// OwnedItem is an item the account owns. Key is the provider item key (Steam appid,
// Audiobookshelf library item ID); ExternalIDs carry item-level identity (steam_app).
type OwnedItem struct {
	Kind            db.MediaKind // "" means db.MediaKindGame
	Key             string
	Title           string
	ExternalIDs     []providers.ExternalID
	PlaytimeMinutes *int // nil when the provider didn't report playtime
	Playtime2Weeks  *int
	LastPlayedAt    *time.Time
	CoverURL        string

	// Used only when the runner creates a manual item. Edition.ExternalIDs (ISBNs)
	// also match existing editions.
	Credits     []providers.Credit
	Edition     *providers.Edition
	Book        *providers.BookDetails
	ReleaseYear int // 0 if unknown

	Progress *Progress // nil when the provider doesn't report progress
}

// OwnedGame is the original name of OwnedItem.
type OwnedGame = OwnedItem

// ItemKind is the observed kind, defaulting to game.
func (g OwnedItem) ItemKind() db.MediaKind {
	if g.Kind == "" {
		return db.MediaKindGame
	}
	return g.Kind
}

// Progress is how far the account is through an item (audiobook listening).
type Progress struct {
	Fraction        float64 // 0–1
	CurrentSeconds  *int32
	DurationSeconds *int32
	Finished        bool
	StartedAt       *time.Time
	FinishedAt      *time.Time
	LastActivityAt  *time.Time
}

// Started reports whether the progress shows any activity.
func (p *Progress) Started() bool {
	return p != nil && (p.Fraction > 0 || p.Finished || (p.CurrentSeconds != nil && *p.CurrentSeconds > 0))
}

// AchievementSet is the full schema and the account's unlocks for one game. Key
// matches OwnedItem.Key; ExternalIDs are a fallback when the game wasn't emitted.
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
	EventProgressChanged        = "progress_changed"
	EventSourceFinished         = "source_finished" // the source reported the item finished
)

// sync_events.field values.
const (
	FieldOwnership = "ownership"
	FieldPlayed    = "played"
	FieldPlaytime  = "playtime_minutes"
	FieldProgress  = "progress" // a fraction, e.g. "0.4300"
	FieldFinished  = "finished" // after is the finish date (YYYY-MM-DD) when known, else "true"
)
