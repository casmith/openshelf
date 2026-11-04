package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/casmith/openshelf/internal/app"
	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/seed"
)

var version = "dev"

const usage = `usage: openshelf <command> [flags]

commands:
  serve [--no-workers]   run the web server (and background workers)
  worker                 run background workers only
  migrate                apply database migrations and exit
  seed                   load demo data (development only)
  user create            create a user account
  version                print the version
  healthcheck            exit 0 if the local server is ready (for container healthchecks)
`

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)

	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		noWorkers := fs.Bool("no-workers", false, "don't run background workers in this process")
		_ = fs.Parse(args)
		return serve(ctx, cfg, log, cfg.Listen, !*noWorkers)
	case "worker":
		return serve(ctx, cfg, log, "", true)
	case "migrate":
		pool, err := app.OpenPool(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		return app.Migrate(ctx, pool, log)
	case "seed":
		if cfg.IsProduction() {
			return errors.New("seed refuses to run when OPENSHELF_ENV=production")
		}
		pool, err := app.OpenPool(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := app.Migrate(ctx, pool, log); err != nil {
			return err
		}
		ran, err := seed.Run(ctx, pool, time.Now())
		if err != nil {
			return err
		}
		if ran {
			fmt.Printf("seeded demo data; sign in as alice or bob with password %q\n", seed.Password)
		} else {
			fmt.Println("demo data already present")
		}
		return nil
	case "user":
		if len(args) == 0 || args[0] != "create" {
			return errors.New("usage: openshelf user create --username NAME [--display-name NAME] [--email EMAIL] [--admin] [--password PW]")
		}
		return createUser(ctx, cfg, args[1:])
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("OPENSHELF_DEBUG") != "" {
		opts.Level = slog.LevelDebug
	}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger, addr string, workers bool) error {
	pool, err := app.OpenPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.AutoMigrate {
		if err := app.Migrate(ctx, pool, log); err != nil {
			return err
		}
	}
	a, err := app.New(cfg, pool, log, app.Options{Workers: workers, SessionCleanup: 30 * time.Minute})
	if err != nil {
		return err
	}
	return a.Serve(ctx, addr)
}

func createUser(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ExitOnError)
	username := fs.String("username", "", "username (lowercase letters, digits, underscore)")
	displayName := fs.String("display-name", "", "display name (defaults to username)")
	email := fs.String("email", "", "email address (optional)")
	admin := fs.Bool("admin", false, "grant admin role")
	password := fs.String("password", os.Getenv("OPENSHELF_USER_PASSWORD"), "password (or OPENSHELF_USER_PASSWORD, or read from stdin)")
	_ = fs.Parse(args)

	if *username == "" {
		return errors.New("--username is required")
	}
	if *displayName == "" {
		*displayName = *username
	}
	if *password == "" {
		fmt.Fprint(os.Stderr, "Password: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read password: %w", err)
		}
		*password = strings.TrimRight(line, "\r\n")
	}
	if len(*password) < auth.MinPassword {
		return fmt.Errorf("password must be at least %d characters", auth.MinPassword)
	}
	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}

	pool, err := app.OpenPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.AutoMigrate {
		if err := app.Migrate(ctx, pool, newLogger(cfg)); err != nil {
			return err
		}
	}
	role := db.UserRoleMember
	if *admin {
		role = db.UserRoleAdmin
	}
	var emailPtr *string
	if *email != "" {
		emailPtr = email
	}
	u, err := db.New(pool).CreateUser(ctx, db.CreateUserParams{
		Username:     strings.ToLower(*username),
		DisplayName:  *displayName,
		Email:        emailPtr,
		PasswordHash: hash,
		Role:         role,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created %s user %q\n", u.Role, u.Username)
	return nil
}

func healthcheck() int {
	host, port, err := net.SplitHostPort(os.Getenv("OPENSHELF_LISTEN"))
	if err != nil {
		port = "8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "readyz:", res.Status)
		return 1
	}
	return 0
}
