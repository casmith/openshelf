package deps

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

// Deps is the shared dependency bundle passed to every feature package.
type Deps struct {
	Config   config.Config
	Pool     *pgxpool.Pool
	Q        *db.Queries
	River    *river.Client[pgx.Tx]
	Sessions *scs.SessionManager
	Log      *slog.Logger
	Now      func() time.Time

	Providers *providers.Registry
}

// WithTx runs fn in a transaction, committing if it returns nil.
func (d *Deps) WithTx(ctx context.Context, fn func(q *db.Queries, tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		return fn(d.Q.WithTx(tx), tx)
	})
}
