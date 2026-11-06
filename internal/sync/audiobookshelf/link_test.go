package audiobookshelf_test

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/audiobookshelf"
	"github.com/casmith/openshelf/internal/testutil"
)

const linkPath = "/connections/audiobookshelf/link"

type acctRow struct {
	ID          int64
	UserID      int64
	ExternalID  string
	DisplayName string
	Credentials []byte
	Status      string
}

func absAccounts(t *testing.T, e *env) []acctRow {
	t.Helper()
	rows, err := e.s.Pool.Query(t.Context(), `select id, user_id, external_account_id, display_name, credentials, status::text
		from connected_accounts where provider = 'audiobookshelf' order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []acctRow
	for rows.Next() {
		var r acctRow
		if err := rows.Scan(&r.ID, &r.UserID, &r.ExternalID, &r.DisplayName, &r.Credentials, &r.Status); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func syncJobs(t *testing.T, e *env, accountID int64) int {
	t.Helper()
	return scalar[int](t, e.s, `select count(*) from river_job where kind = 'sync_account' and (args->>'account_id')::bigint = $1`, accountID)
}

func (e *env) decrypt(t *testing.T, c []byte) string {
	t.Helper()
	b, err := e.s.Deps.Secrets.Decrypt(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLinkAccount(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	c := e.s.LoginAs(t, alice)

	page := c.Get("/settings/connections")
	if !strings.Contains(page.Body, linkPath) || !strings.Contains(page.Body, "Audiobookshelf") {
		t.Fatal("connections page doesn't offer Audiobookshelf")
	}
	form := c.Get(linkPath)
	if form.StatusCode != http.StatusOK || !strings.Contains(text(form.Body), "Audiobookshelf → Settings → API Keys") {
		t.Fatalf("form: %d", form.StatusCode)
	}

	// A wrong key is refused and nothing is stored or echoed.
	res := c.PostForm(linkPath, url.Values{"api_key": {"wrong-key-123"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "didn") || strings.Contains(res.Body, "wrong-key-123") {
		t.Fatalf("bad key: %d", res.StatusCode)
	}
	if res := c.PostForm(linkPath, url.Values{"api_key": {""}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty key: %d", res.StatusCode)
	}
	if n := len(absAccounts(t, e)); n != 0 {
		t.Fatalf("%d accounts after failures", n)
	}

	res = c.PostForm(linkPath, url.Values{"api_key": {aliceKey}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/settings/connections" {
		t.Fatalf("link: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	accts := absAccounts(t, e)
	if len(accts) != 1 {
		t.Fatalf("%d accounts", len(accts))
	}
	a := accts[0]
	if a.UserID != alice.ID || a.ExternalID != aliceABSID || a.DisplayName != "alice_abs" || a.Status != "active" {
		t.Errorf("account %+v", a)
	}
	if bytes.Contains(a.Credentials, []byte(aliceKey)) || e.decrypt(t, a.Credentials) != aliceKey {
		t.Error("key isn't stored encrypted")
	}
	if syncJobs(t, e, a.ID) != 1 {
		t.Error("no sync enqueued")
	}
	if page := c.Get("/settings/connections").Body; !strings.Contains(page, "alice_abs") || strings.Contains(page, aliceKey) {
		t.Error("connections page: account missing or key rendered")
	}

	// Re-linking with a new key for the same Audiobookshelf user refreshes it.
	e.abs.setUser("alice-new-key-456", meBody(aliceABSID, "alice_abs", ""))
	if _, err := e.s.Pool.Exec(t.Context(), `update connected_accounts set status = 'error', last_error = 'rejected' where id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if res := c.PostForm(linkPath, url.Values{"api_key": {"alice-new-key-456"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("re-link: %d", res.StatusCode)
	}
	accts = absAccounts(t, e)
	if len(accts) != 1 || e.decrypt(t, accts[0].Credentials) != "alice-new-key-456" || accts[0].Status != "active" {
		t.Errorf("after re-link %+v", accts)
	}

	// Another OpenShelf user can't claim the same Audiobookshelf user.
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	e.abs.setUser("bob-steals", meBody(aliceABSID, "alice_abs", ""))
	res = e.s.LoginAs(t, bob).PostForm(linkPath, url.Values{"api_key": {"bob-steals"}})
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.Body, "another OpenShelf user") {
		t.Fatalf("duplicate: %d", res.StatusCode)
	}
	accts = absAccounts(t, e)
	if len(accts) != 1 || accts[0].UserID != alice.ID || e.decrypt(t, accts[0].Credentials) != "alice-new-key-456" {
		t.Errorf("duplicate link changed the account: %+v", accts)
	}
}

func TestLinkUnavailable(t *testing.T) {
	e := newEnv(t)
	e.ad.BaseURL = ""
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	c := e.s.LoginAs(t, alice)
	if res := c.Get(linkPath); res.StatusCode != http.StatusSeeOther {
		t.Errorf("unconfigured GET: %d", res.StatusCode)
	}
	if res := c.PostForm(linkPath, url.Values{"api_key": {aliceKey}}); res.StatusCode != http.StatusSeeOther || len(absAccounts(t, e)) != 0 {
		t.Errorf("unconfigured POST: %d", res.StatusCode)
	}
	if res := e.s.Client(t).Get(linkPath); res.StatusCode == http.StatusOK {
		t.Error("logged-out user reached the link form")
	}
}

func TestLinkServerDown(t *testing.T) {
	e := newEnv(t)
	e.abs.Status = http.StatusBadGateway
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	res := e.s.LoginAs(t, alice).PostForm(linkPath, url.Values{"api_key": {aliceKey}})
	if res.StatusCode != http.StatusBadGateway || strings.Contains(res.Body, aliceKey) || len(absAccounts(t, e)) != 0 {
		t.Errorf("server down: %d", res.StatusCode)
	}
}

func TestDefaultAdapterConfig(t *testing.T) {
	cfg := testutil.TestConfig()
	if audiobookshelf.New(cfg, nil).Configured() {
		t.Error("configured without a URL")
	}
	cfg.AudiobookshelfURL = "http://abs:13378"
	s := testutil.NewServer(t)
	a := audiobookshelf.New(cfg, s.Deps.Secrets)
	if !a.Configured() {
		t.Error("not configured with a URL")
	}
	var _ sync.Adapter = a
	if a.Capabilities().LinkPath != linkPath || !a.Capabilities().Progress {
		t.Errorf("capabilities %+v", a.Capabilities())
	}
	if _, ok := s.Deps.SyncAdapters.Get(audiobookshelf.Provider); !ok {
		t.Error("app doesn't register the adapter")
	}
}
