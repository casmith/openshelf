package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/migrations"
)

// DefaultAdminURL points at the dev compose Postgres; override with OPENSHELF_TEST_DATABASE_URL.
const DefaultAdminURL = "postgres://openshelf:openshelf@localhost:5433/postgres?sslmode=disable"

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

func adminURL() string {
	if v := os.Getenv("OPENSHELF_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return DefaultAdminURL
}

func withDatabase(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// migrationsHash names the template so it's rebuilt whenever migrations change.
func migrationsHash() (string, error) {
	h := sha256.New()
	var names []string
	err := fs.WalkDir(migrations.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := fs.ReadFile(migrations.FS(), n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", n, b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func ensureTemplate(ctx context.Context) (string, error) {
	templateOnce.Do(func() {
		hash, err := migrationsHash()
		if err != nil {
			templateErr = err
			return
		}
		templateName = "openshelf_tmpl_" + hash
		admin, err := pgx.Connect(ctx, adminURL())
		if err != nil {
			templateErr = fmt.Errorf("connect to test postgres (is `make db-up` running?): %w", err)
			return
		}
		defer admin.Close(ctx)

		// Serialize template creation across concurrently running test binaries.
		if _, err := admin.Exec(ctx, "select pg_advisory_lock(727274)"); err != nil {
			templateErr = err
			return
		}
		defer admin.Exec(ctx, "select pg_advisory_unlock(727274)") //nolint:errcheck

		var exists bool
		if err := admin.QueryRow(ctx, "select exists(select 1 from pg_database where datname = $1)", templateName).Scan(&exists); err != nil {
			templateErr = err
			return
		}
		if exists {
			return
		}
		building := templateName + "_build"
		_, _ = admin.Exec(ctx, "drop database if exists "+pgx.Identifier{building}.Sanitize()+" with (force)")
		if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{building}.Sanitize()); err != nil {
			templateErr = err
			return
		}
		pool, err := pgxpool.New(ctx, withDatabase(adminURL(), building))
		if err != nil {
			templateErr = err
			return
		}
		err = migrations.Up(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
		pool.Close()
		if err != nil {
			templateErr = fmt.Errorf("migrate template: %w", err)
			return
		}
		_, templateErr = admin.Exec(ctx, "alter database "+pgx.Identifier{building}.Sanitize()+" rename to "+pgx.Identifier{templateName}.Sanitize())
	})
	return templateName, templateErr
}

// NewDB returns a pool to a fresh, fully migrated database that is dropped after the test.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	tmpl, err := ensureTemplate(ctx)
	if err != nil {
		t.Fatalf("test database: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "openshelf_test_" + hex.EncodeToString(b)

	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{name}.Sanitize()+" template "+pgx.Identifier{tmpl}.Sanitize()); err != nil {
		t.Fatalf("clone template: %v", err)
	}

	pc, err := pgxpool.ParseConfig(withDatabase(adminURL(), name))
	if err != nil {
		t.Fatalf("parse test db url: %v", err)
	}
	tr := &queryTracer{}
	pc.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	tracers.Store(pool, tr)
	t.Cleanup(func() {
		tracers.Delete(pool)
		pool.Close()
		c, err := pgx.Connect(context.Background(), adminURL())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "drop database if exists "+pgx.Identifier{name}.Sanitize()+" with (force)")
	})
	return pool
}
