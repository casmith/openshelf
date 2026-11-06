package connections_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

func jobs(t *testing.T, s *testutil.Server) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from river_job where kind = 'sync_account'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPageListsAdaptersAndAccounts(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "steam", "76561197960287930")
	if _, err := s.Pool.Exec(context.Background(), `
		insert into sync_runs (account_id, status, warnings, finished_at)
		values ($1, 'partial', '{"Game details are private"}', now())`, acct.ID); err != nil {
		t.Fatal(err)
	}

	res := s.LoginAs(t, alice).Get("/settings/connections")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for _, want := range []string{"Steam", "an administrator needs to configure it", acct.PublicID.String(),
		"Last sync was partial", "Game details are private", "/settings/connections/" + acct.PublicID.String() + "/history",
		"/connections/" + acct.PublicID.String() + "/unlink"} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(res.Body, `href="/connections/steam/link"`) {
		t.Error("link button shown for an unconfigured adapter")
	}

	if body := s.LoginAs(t, bob).Get("/settings/connections").Body; strings.Contains(body, acct.PublicID.String()) {
		t.Fatal("bob sees alice's account")
	}
	if res := s.Client(t).Get("/settings/connections"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: status %d", res.StatusCode)
	}

	s.Deps.SyncAdapters = sync.NewRegistry(&synctest.Fake{Name: "steam", Caps: sync.Capabilities{DisplayName: "Steam", LinkPath: "/connections/steam/link"}})
	if body := s.LoginAs(t, alice).Get("/settings/connections").Body; !strings.Contains(body, `href="/connections/steam/link"`) {
		t.Error("link button missing for a configured adapter")
	}
}

func TestSyncNow(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	bob := testutil.CreateUser(t, s.Q, "bob")
	acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, "fake", "123")
	path := "/settings/connections/" + acct.PublicID.String() + "/sync"
	ac := s.LoginAs(t, alice)

	s.Deps.SyncAdapters = sync.NewRegistry(&synctest.Fake{Unconfigured: true})
	if res := ac.PostForm(path, nil); res.StatusCode != http.StatusSeeOther || jobs(t, s) != 0 {
		t.Fatalf("unconfigured: status %d jobs %d", res.StatusCode, jobs(t, s))
	}
	if !strings.Contains(ac.Get("/settings/connections").Body, "administrator configures it") {
		t.Error("missing unconfigured flash")
	}

	s.Deps.SyncAdapters = sync.NewRegistry(&synctest.Fake{})
	if res := s.LoginAs(t, bob).PostForm(path, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("bob: status %d", res.StatusCode)
	}
	if res := ac.PostForm("/settings/connections/not-a-uuid/sync", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("bad id: status %d", res.StatusCode)
	}
	for range 2 {
		if res := ac.PostForm(path, nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/settings/connections" {
			t.Fatalf("sync now: status %d", res.StatusCode)
		}
	}
	if n := jobs(t, s); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	if !strings.Contains(ac.Get("/settings/connections").Body, "Sync started") {
		t.Error("missing flash")
	}
}

func TestSettingsLinksToConnections(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	if body := s.LoginAs(t, u).Get("/settings").Body; !strings.Contains(body, `href="/settings/connections"`) {
		t.Fatal("settings page doesn't link to connections")
	}
}
