package app

import (
	"errors"
	"log/slog"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/secrets"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/steam"
)

func newSyncAdapters(cfg config.Config, metadata *providers.Registry) *sync.Registry {
	st := steam.New(cfg)
	st.Metadata = metadata
	return sync.NewRegistry(st)
}

func newSecrets(cfg config.Config, log *slog.Logger) (*secrets.Box, error) {
	key := cfg.SecretKey
	if key == "" {
		if cfg.IsProduction() {
			return nil, errors.New("OPENSHELF_SECRET_KEY is required in production")
		}
		if cfg.Env != "test" {
			log.Warn("OPENSHELF_SECRET_KEY is not set; using an insecure development key for stored credentials")
		}
		key = secrets.DevKey
	}
	return secrets.New(key, cfg.SecretKeyPrevious)
}
