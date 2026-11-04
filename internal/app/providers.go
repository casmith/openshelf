package app

import (
	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/igdb"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
	"github.com/casmith/openshelf/internal/providers/tmdb"
)

func newProviders(cfg config.Config) *providers.Registry {
	return providers.NewRegistry(
		openlibrary.New(cfg),
		tmdb.New(cfg),
		igdb.New(cfg),
	)
}
