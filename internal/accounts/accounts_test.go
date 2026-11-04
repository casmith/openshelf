package accounts_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/accounts"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func wantStatus(t *testing.T, res *testutil.Response, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, res.Body)
	}
}

func getUser(t *testing.T, s *testutil.Server, username string) db.User {
	t.Helper()
	u, err := s.Q.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func login(s *testutil.Server, t *testing.T, username, password string) (*testutil.Client, *testutil.Response) {
	c := s.Client(t)
	return c, c.PostForm("/login", url.Values{"username": {username}, "password": {password}})
}

var tempPasswordRE = regexp.MustCompile(`data-temp-password>([^<]+)<`)

func tempPassword(t *testing.T, body string) string {
	t.Helper()
	m := tempPasswordRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no temporary password in page:\n%s", body)
	}
	return m[1]
}

func TestSetupFirstRun(t *testing.T) {
	s := testutil.NewServer(t)
	s.Deps.Config.SteamAPIKey = "steam-key"
	c := s.Client(t)

	res := c.Get("/setup")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, `href="/login"`) {
		t.Error("setup page should link to /login")
	}
	wantStatus(t, c.Get("/login"), http.StatusOK)

	res = c.PostForm("/setup", url.Values{"username": {"Bad Name"}, "password": {"short"}, "password_confirm": {"short"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res = c.PostForm("/setup", url.Values{"username": {"alice"}, "password": {"password123"}, "password_confirm": {"password124"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)

	res = c.PostForm("/setup", url.Values{"username": {"Alice"}, "display_name": {"Alice A"}, "email": {"alice@example.com"},
		"password": {"password123"}, "password_confirm": {"password123"}})
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"OPENSHELF_", "Open Library", "TMDB", "IGDB", "Steam", `data-provider="Steam"`} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("setup result missing %q", want)
		}
	}
	if !regexp.MustCompile(`data-provider="Steam"[\s\S]*?data-configured="true"`).MatchString(res.Body) {
		t.Error("Steam should show as configured")
	}
	if !regexp.MustCompile(`data-provider="TMDB"[\s\S]*?data-configured="false"`).MatchString(res.Body) {
		t.Error("TMDB should show as not configured without a token")
	}

	u := getUser(t, s, "alice")
	if u.Role != db.UserRoleAdmin || u.DisplayName != "Alice A" {
		t.Fatalf("setup user = %+v", u)
	}
	wantStatus(t, c.Get("/settings"), http.StatusOK)

	wantStatus(t, c.Get("/setup"), http.StatusNotFound)
	wantStatus(t, s.Client(t).Get("/setup"), http.StatusNotFound)
	res = s.Client(t).PostForm("/setup", url.Values{"username": {"mallory"}, "password": {"password123"}, "password_confirm": {"password123"}})
	wantStatus(t, res, http.StatusNotFound)
	if n, _ := s.Q.CountUsers(context.Background()); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func TestSetup404WhenAnyUserExists(t *testing.T) {
	s := testutil.NewServer(t)
	testutil.CreateUser(t, s.Q, "bob")
	wantStatus(t, s.Client(t).Get("/setup"), http.StatusNotFound)
	_, err := accounts.Setup(context.Background(), s.Deps, accounts.NewUser{Username: "eve", DisplayName: "Eve", Password: "password123"})
	if !errors.Is(err, accounts.ErrSetupDone) {
		t.Fatalf("Setup err = %v, want ErrSetupDone", err)
	}
}

func TestSettingsProfileAndPreferences(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	if _, err := s.Pool.Exec(context.Background(), `update users set email = 'bob@example.com' where id = $1`, bob.ID); err != nil {
		t.Fatal(err)
	}
	c := s.LoginAs(t, alice)

	res := c.Get("/settings")
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{`action="/settings/profile"`, `action="/settings/password"`, `action="/settings/preferences"`, "Europe/Berlin"} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	if strings.Contains(res.Body, `href="/admin/users"`) {
		t.Error("members shouldn't see the admin link")
	}

	wantStatus(t, c.PostForm("/settings/profile", url.Values{"display_name": {""}}), http.StatusUnprocessableEntity)
	wantStatus(t, c.PostForm("/settings/profile", url.Values{"display_name": {"Al"}, "email": {"nope"}}), http.StatusUnprocessableEntity)
	res = c.PostForm("/settings/profile", url.Values{"display_name": {"Al"}, "email": {"BOB@example.com"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(res.Body, "already in use") {
		t.Error("duplicate email should be reported")
	}

	res = c.PostForm("/settings/profile", url.Values{"display_name": {"Alice Liddell"}, "email": {"alice@example.com"}, "username": {"queen"}})
	wantStatus(t, res, http.StatusSeeOther)
	u := getUser(t, s, "alice")
	if u.DisplayName != "Alice Liddell" || u.Email == nil || *u.Email != "alice@example.com" {
		t.Fatalf("profile not saved: %+v", u)
	}
	if b := getUser(t, s, "bob"); b.DisplayName != bob.DisplayName {
		t.Fatal("another user's profile changed")
	}

	wantStatus(t, c.PostForm("/settings/preferences", url.Values{"default_visibility": {"instance"}, "rating_display": {"ten"}, "timezone": {"Mars/Olympus"}}), http.StatusUnprocessableEntity)
	wantStatus(t, c.PostForm("/settings/preferences", url.Values{"default_visibility": {"everyone"}, "rating_display": {"ten"}, "timezone": {"UTC"}}), http.StatusUnprocessableEntity)
	res = c.PostForm("/settings/preferences", url.Values{"default_visibility": {"instance"}, "rating_display": {"ten"}, "timezone": {"America/Indiana/Knox"}})
	wantStatus(t, res, http.StatusSeeOther)
	u = getUser(t, s, "alice")
	if u.DefaultVisibility != db.VisibilityInstance || u.RatingDisplay != db.RatingDisplayTen || u.Timezone != "America/Indiana/Knox" {
		t.Fatalf("preferences not saved: %+v", u)
	}
	res = c.Get("/settings")
	if !strings.Contains(res.Body, `<option value="America/Indiana/Knox" selected`) || !strings.Contains(res.Body, "Preferences saved.") {
		t.Error("settings should show the saved non-curated zone and a flash")
	}
}

func TestChangePasswordRequiresCurrent(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)

	res := c.PostForm("/settings/password", url.Values{"new_password": {"brand new pw"}, "new_password_confirm": {"brand new pw"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	res = c.PostForm("/settings/password", url.Values{"current_password": {"wrong password"}, "new_password": {"brand new pw"}, "new_password_confirm": {"brand new pw"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(res.Body, "Current password is incorrect.") {
		t.Error("expected incorrect-current-password error")
	}
	res = c.PostForm("/settings/password", url.Values{"current_password": {testutil.TestPassword}, "new_password": {"short"}, "new_password_confirm": {"short"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if getUser(t, s, "alice").PasswordHash != alice.PasswordHash {
		t.Fatal("password changed despite validation errors")
	}

	res = c.PostForm("/settings/password", url.Values{"current_password": {testutil.TestPassword}, "new_password": {"brand new pw"}, "new_password_confirm": {"brand new pw"}})
	wantStatus(t, res, http.StatusSeeOther)
	wantStatus(t, c.Get("/settings"), http.StatusOK)

	_, res = login(s, t, "alice", testutil.TestPassword)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	_, res = login(s, t, "alice", "brand new pw")
	wantStatus(t, res, http.StatusSeeOther)
}

func TestMemberForbiddenOnAdmin(t *testing.T) {
	s := testutil.NewServer(t)
	testutil.CreateUser(t, s.Q, "alice", testutil.Admin)
	bob := testutil.CreateUser(t, s.Q, "bob")
	c := s.LoginAs(t, bob)

	for _, p := range []string{"/admin/users", "/admin/users/new"} {
		wantStatus(t, c.Get(p), http.StatusForbidden)
		wantStatus(t, s.Client(t).Get(p), http.StatusSeeOther)
	}
	posts := map[string]url.Values{
		"/admin/users":                      {"username": {"eve"}},
		"/admin/users/alice/role":           {"role": {"member"}},
		"/admin/users/bob/role":             {"role": {"admin"}},
		"/admin/users/alice/disable":        nil,
		"/admin/users/alice/enable":         nil,
		"/admin/users/alice/reset-password": nil,
	}
	for p, v := range posts {
		wantStatus(t, c.PostForm(p, v), http.StatusForbidden)
	}
	if u := getUser(t, s, "alice"); u.Role != db.UserRoleAdmin || u.DisabledAt != nil {
		t.Fatal("member changed an admin")
	}
	if getUser(t, s, "bob").Role != db.UserRoleMember {
		t.Fatal("member promoted themselves")
	}
	if n, _ := s.Q.CountUsers(context.Background()); n != 2 {
		t.Fatal("member created a user")
	}
}

func TestAdminCreateAndResetPassword(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice", testutil.Admin)
	c := s.LoginAs(t, alice)

	res := c.Get("/settings")
	if !strings.Contains(res.Body, `href="/admin/users"`) {
		t.Error("admins should see the admin link in settings")
	}

	wantStatus(t, c.PostForm("/admin/users", url.Values{"username": {"x"}}), http.StatusUnprocessableEntity)
	res = c.PostForm("/admin/users", url.Values{"username": {"alice"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(res.Body, "taken") {
		t.Error("duplicate username should be reported")
	}

	res = c.PostForm("/admin/users", url.Values{"username": {"carol"}, "display_name": {"Carol"}, "email": {"carol@example.com"}})
	wantStatus(t, res, http.StatusOK)
	pw := tempPassword(t, res.Body)
	carol := getUser(t, s, "carol")
	if carol.Role != db.UserRoleMember {
		t.Fatalf("carol role = %s", carol.Role)
	}
	_, res = login(s, t, "carol", pw)
	wantStatus(t, res, http.StatusSeeOther)

	res = c.PostForm("/admin/users", url.Values{"username": {"dave"}, "admin": {"on"}})
	wantStatus(t, res, http.StatusOK)
	if getUser(t, s, "dave").Role != db.UserRoleAdmin {
		t.Fatal("dave should be an admin")
	}

	res = c.Get("/admin/users")
	wantStatus(t, res, http.StatusOK)
	for _, name := range []string{"@alice", "@carol", "@dave", "carol@example.com"} {
		if !strings.Contains(res.Body, name) {
			t.Errorf("user list missing %q", name)
		}
	}
	if strings.Contains(res.Body, carol.PasswordHash) {
		t.Fatal("user list leaks password hashes")
	}

	res = c.PostForm("/admin/users/carol/reset-password", nil)
	wantStatus(t, res, http.StatusOK)
	newPW := tempPassword(t, res.Body)
	if newPW == pw {
		t.Fatal("reset should generate a new password")
	}
	_, res = login(s, t, "carol", pw)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	_, res = login(s, t, "carol", newPW)
	wantStatus(t, res, http.StatusSeeOther)

	wantStatus(t, c.PostForm("/admin/users/nobody/reset-password", nil), http.StatusNotFound)
	wantStatus(t, c.PostForm("/admin/users/nobody/disable", nil), http.StatusNotFound)
}

func TestDisabledUserIsLoggedOut(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice", testutil.Admin)
	bob := testutil.CreateUser(t, s.Q, "bob")
	admin := s.LoginAs(t, alice)
	bc := s.LoginAs(t, bob)
	wantStatus(t, bc.Get("/settings"), http.StatusOK)

	wantStatus(t, admin.PostForm("/admin/users/bob/disable", nil), http.StatusSeeOther)
	if getUser(t, s, "bob").DisabledAt == nil {
		t.Fatal("bob not disabled")
	}
	res := bc.Get("/settings")
	wantStatus(t, res, http.StatusSeeOther)
	if !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("redirect to %q, want /login", res.Header.Get("Location"))
	}
	_, res = login(s, t, "bob", testutil.TestPassword)
	wantStatus(t, res, http.StatusUnprocessableEntity)

	wantStatus(t, admin.PostForm("/admin/users/bob/enable", nil), http.StatusSeeOther)
	if getUser(t, s, "bob").DisabledAt != nil {
		t.Fatal("bob not re-enabled")
	}
	wantStatus(t, bc.Get("/settings"), http.StatusSeeOther)
	_, res = login(s, t, "bob", testutil.TestPassword)
	wantStatus(t, res, http.StatusSeeOther)
}

func TestLastAdminAndSelfGuards(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice", testutil.Admin)
	bob := testutil.CreateUser(t, s.Q, "bob")
	ac := s.LoginAs(t, alice)

	wantStatus(t, ac.PostForm("/admin/users/alice/disable", nil), http.StatusSeeOther)
	res := ac.Get("/admin/users")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "You can&#39;t disable your own account.") {
		t.Errorf("expected self-disable refusal flash:\n%s", res.Body)
	}
	wantStatus(t, ac.PostForm("/admin/users/alice/role", url.Values{"role": {"member"}}), http.StatusSeeOther)
	res = ac.Get("/admin/users")
	if !strings.Contains(res.Body, "The last admin can&#39;t be demoted or disabled.") {
		t.Errorf("expected last-admin refusal flash:\n%s", res.Body)
	}
	if u := getUser(t, s, "alice"); u.Role != db.UserRoleAdmin || u.DisabledAt != nil {
		t.Fatalf("last admin changed: %+v", u)
	}
	if err := accounts.SetDisabled(context.Background(), s.Deps, bob, "alice", true); !errors.Is(err, accounts.ErrLastAdmin) {
		t.Fatalf("disabling the last admin: err = %v, want ErrLastAdmin", err)
	}

	wantStatus(t, ac.PostForm("/admin/users/bob/role", url.Values{"role": {"admin"}}), http.StatusSeeOther)
	res = ac.PostForm("/admin/users/alice/role", url.Values{"role": {"member"}})
	wantStatus(t, res, http.StatusSeeOther)
	if res.Header.Get("Location") != "/settings" {
		t.Errorf("self-demotion should redirect to /settings, got %q", res.Header.Get("Location"))
	}
	if getUser(t, s, "alice").Role != db.UserRoleMember {
		t.Fatal("alice should be able to step down while another admin exists")
	}
	wantStatus(t, ac.Get("/admin/users"), http.StatusForbidden)

	bc := s.LoginAs(t, bob)
	wantStatus(t, bc.PostForm("/admin/users/bob/role", url.Values{"role": {"member"}}), http.StatusSeeOther)
	if getUser(t, s, "bob").Role != db.UserRoleAdmin {
		t.Fatal("bob is now the last admin and must stay admin")
	}
	wantStatus(t, bc.PostForm("/admin/users/alice/disable", nil), http.StatusSeeOther)
	if getUser(t, s, "alice").DisabledAt == nil {
		t.Fatal("admin should be able to disable a member")
	}
	wantStatus(t, bc.PostForm("/admin/users/alice/role", url.Values{"role": {"owner"}}), http.StatusBadRequest)
}
