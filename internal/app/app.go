package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/migrations"
	"github.com/casmith/openshelf/internal/server"
)

type Options struct {
	Workers bool // run River workers in this process
	// SessionCleanup is how often expired sessions are purged; 0 disables it.
	SessionCleanup time.Duration
}

type App struct {
	Deps    *deps.Deps
	Handler http.Handler
	workers bool
}

func OpenPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if pc.MaxConns > 10 {
		pc.MaxConns = 10
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// New wires dependencies around an existing pool. The caller owns the pool.
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, opts Options) (*App, error) {
	sm := scs.New()
	sm.Store = pgxstore.NewWithCleanupInterval(pool, opts.SessionCleanup)
	sm.Lifetime = 30 * 24 * time.Hour
	sm.Cookie.Name = "openshelf_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Secure = cfg.SecureCookies()
	sm.Cookie.Persist = true

	d := &deps.Deps{
		Config:    cfg,
		Pool:      pool,
		Q:         db.New(pool),
		Sessions:  sm,
		Log:       log,
		Now:       time.Now,
		Providers: newProviders(cfg),
	}

	workers := river.NewWorkers()
	registerWorkers(workers, d)
	rc := &river.Config{
		Logger:            log.With("component", "river"),
		FetchPollInterval: 5 * time.Second,
		Workers:           workers,
	}
	if opts.Workers {
		rc.Queues = map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}}
		rc.PeriodicJobs = periodicJobs(d)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), rc)
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	d.River = client

	return &App{Deps: d, Handler: server.New(d), workers: opts.Workers}, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	return migrations.Up(ctx, pool, log)
}

// Serve runs the HTTP server (if addr is set) and workers until ctx is cancelled.
func (a *App) Serve(ctx context.Context, addr string) error {
	if a.workers {
		if err := a.Deps.River.Start(ctx); err != nil {
			return fmt.Errorf("start workers: %w", err)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = a.Deps.River.Stop(sctx)
		}()
	}
	if addr == "" {
		<-ctx.Done()
		return nil
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(a.Deps.Log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() {
		a.Deps.Log.Info("listening", "addr", addr, "workers", a.workers)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}
