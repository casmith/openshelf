package testutil

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/app"
	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

type Server struct {
	*httptest.Server
	Deps *deps.Deps
	Pool *pgxpool.Pool
	Q    *db.Queries
}

// TestPassword is the password CreateUser assigns.
const TestPassword = "correct horse battery"

func TestConfig() config.Config {
	return config.Config{
		Env:         "test",
		BaseURL:     "http://openshelf.test",
		DataDir:     "",
		AutoMigrate: false,
	}
}

// NewServer starts the full application over httptest against a fresh database.
// Workers don't run; use River's insert-only client to assert enqueued jobs.
func NewServer(t testing.TB) *Server {
	t.Helper()
	pool := NewDB(t)
	cfg := TestConfig()
	cfg.DataDir = t.TempDir()
	a, err := app.New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), app.Options{})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	return &Server{Server: srv, Deps: a.Deps, Pool: pool, Q: a.Deps.Q}
}

type UserOpt func(*db.CreateUserParams)

func Admin(p *db.CreateUserParams) { p.Role = db.UserRoleAdmin }

func CreateUser(t testing.TB, q *db.Queries, username string, opts ...UserOpt) db.User {
	t.Helper()
	hash, err := auth.HashPassword(TestPassword)
	if err != nil {
		t.Fatal(err)
	}
	p := db.CreateUserParams{Username: username, DisplayName: strings.ToUpper(username[:1]) + username[1:], PasswordHash: hash, Role: db.UserRoleMember}
	for _, o := range opts {
		o(&p)
	}
	u, err := q.CreateUser(context.Background(), p)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

type Client struct {
	t    testing.TB
	base string
	HTTP *http.Client
}

type Response struct {
	*http.Response
	Body string
}

// Client returns an HTTP client with its own cookie jar that doesn't follow redirects.
func (s *Server) Client(t testing.TB) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{t: t, base: s.URL, HTTP: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// LoginAs returns a client already signed in as u (created with CreateUser).
func (s *Server) LoginAs(t testing.TB, u db.User) *Client {
	t.Helper()
	c := s.Client(t)
	res := c.PostForm("/login", url.Values{"username": {u.Username}, "password": {TestPassword}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login as %s: status %d\n%s", u.Username, res.StatusCode, res.Body)
	}
	return c
}

func (c *Client) Do(req *http.Request) *Response {
	c.t.Helper()
	res, err := c.HTTP.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &Response{Response: res, Body: string(b)}
}

func (c *Client) Get(path string) *Response {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	return c.Do(req)
}

func (c *Client) PostForm(path string, v url.Values) *Response {
	c.t.Helper()
	return c.Form(http.MethodPost, path, v, false)
}

// Form sends form-encoded values; htmx adds the HX-Request header of a partial request.
func (c *Client) Form(method, path string, v url.Values, htmx bool) *Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return c.Do(req)
}
