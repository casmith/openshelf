package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Env         string // "development", "production", "test"
	DatabaseURL string
	Listen      string
	BaseURL     string
	DataDir     string
	SecretKey   string
	AutoMigrate bool

	TMDBToken        string
	IGDBClientID     string
	IGDBClientSecret string
	SteamAPIKey      string
}

func Load() (Config, error) {
	c := Config{
		Env:              env("OPENSHELF_ENV", "development"),
		DatabaseURL:      env("OPENSHELF_DATABASE_URL", "postgres://openshelf:openshelf@localhost:5433/openshelf?sslmode=disable"),
		Listen:           env("OPENSHELF_LISTEN", ":8080"),
		BaseURL:          env("OPENSHELF_BASE_URL", "http://localhost:8080"),
		DataDir:          env("OPENSHELF_DATA_DIR", "./data"),
		SecretKey:        os.Getenv("OPENSHELF_SECRET_KEY"),
		TMDBToken:        os.Getenv("OPENSHELF_TMDB_TOKEN"),
		IGDBClientID:     os.Getenv("OPENSHELF_IGDB_CLIENT_ID"),
		IGDBClientSecret: os.Getenv("OPENSHELF_IGDB_CLIENT_SECRET"),
		SteamAPIKey:      os.Getenv("OPENSHELF_STEAM_API_KEY"),
	}
	var err error
	if c.AutoMigrate, err = boolEnv("OPENSHELF_AUTO_MIGRATE", true); err != nil {
		return c, err
	}
	switch c.Env {
	case "development", "production", "test":
	default:
		return c, fmt.Errorf("OPENSHELF_ENV: unknown environment %q", c.Env)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c, fmt.Errorf("OPENSHELF_BASE_URL: invalid URL %q", c.BaseURL)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.IsProduction() && len(c.SecretKey) < 32 {
		return c, fmt.Errorf("OPENSHELF_SECRET_KEY must be at least 32 characters in production")
	}
	return c, nil
}

func (c Config) IsProduction() bool { return c.Env == "production" }

func (c Config) SecureCookies() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
