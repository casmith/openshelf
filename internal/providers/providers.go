// Package providers defines the metadata adapter contract. Adapters normalize
// provider responses into Item; the discover package imports them into the catalog.
package providers

import (
	"context"
	"errors"
	"time"

	"github.com/casmith/openshelf/internal/db"
)

var ErrNotFound = errors.New("providers: not found")

type Provider interface {
	Name() string // "openlibrary", "tmdb", "igdb"
	Kinds() []db.MediaKind
	Configured() bool // false when required credentials are missing
	Search(ctx context.Context, kind db.MediaKind, query string) ([]SearchResult, error)
	Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*Item, error)
}

// ExternalID uses the namespaced provider keys from docs/data-model.md (e.g. "tmdb_movie", "isbn13").
type ExternalID struct {
	Provider string
	Value    string
	URL      string
}

type SearchResult struct {
	Provider   string
	ExternalID ExternalID // the ID to pass to Fetch
	Kind       db.MediaKind
	Title      string
	Year       int    // 0 if unknown
	Subtitle   string // author, director, platforms...
	CoverURL   string
}

type Credit struct {
	Name string
	Role db.CreditRole
}

// PlatformRef identifies a game platform; the importer resolves it via external_ids, then aliases.
type PlatformRef struct {
	ExternalID *ExternalID // e.g. igdb_platform
	Name       string
}

type Edition struct {
	Title           string
	Format          db.EditionFormat // "" if unknown
	Platform        *PlatformRef
	Region          string
	Publisher       string
	ReleaseDate     *time.Time
	PageCount       *int32
	DurationMinutes *int32
	CoverURL        string
	ExternalIDs     []ExternalID
}

type GameDetails struct {
	Franchise  string
	TimeToBeat *TimeToBeat // nil when the provider has no estimate
}

// TimeToBeat holds community playtime estimates in minutes; any of them may be unknown.
type TimeToBeat struct {
	HastilyMinutes    *int32
	NormallyMinutes   *int32
	CompletelyMinutes *int32
	Submissions       int32
}

type BookDetails struct {
	SeriesName     string
	SeriesPosition *float64
}

type FilmDetails struct{ RuntimeMinutes *int32 }

type ShowDetails struct {
	Network      string
	Status       string
	SeasonCount  *int32
	EpisodeCount *int32
}

type Item struct {
	Kind          db.MediaKind
	Title         string
	OriginalTitle string
	ReleaseDate   *time.Time
	ReleaseYear   int // 0 if unknown
	Synopsis      string
	Genres        []string // lowercase
	CoverURL      string
	Credits       []Credit
	Editions      []Edition
	ExternalIDs   []ExternalID // first is the provider's primary ID for this item
	Attribution   string

	Game *GameDetails
	Book *BookDetails
	Film *FilmDetails
	Show *ShowDetails
}

type Registry struct {
	list []Provider
}

func NewRegistry(ps ...Provider) *Registry {
	return &Registry{list: ps}
}

func (r *Registry) All() []Provider { return r.list }

// For returns configured providers that support kind, in registration order.
func (r *Registry) For(kind db.MediaKind) []Provider {
	var out []Provider
	for _, p := range r.list {
		if !p.Configured() {
			continue
		}
		for _, k := range p.Kinds() {
			if k == kind {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func (r *Registry) Get(name string) (Provider, bool) {
	for _, p := range r.list {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}
