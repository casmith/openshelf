package openlibrary

import (
	"context"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

// Provider is the Open Library adapter. Stub until its task lands (docs/plan.md).
type Provider struct{ cfg config.Config }

func New(cfg config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string          { return "openlibrary" }
func (p *Provider) Kinds() []db.MediaKind { return []db.MediaKind{db.MediaKindBook} }
func (p *Provider) Configured() bool      { return false }

func (p *Provider) Search(ctx context.Context, kind db.MediaKind, query string) ([]providers.SearchResult, error) {
	return nil, nil
}

func (p *Provider) Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*providers.Item, error) {
	return nil, providers.ErrNotFound
}
