package steam_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/steam"
	"github.com/casmith/openshelf/internal/testutil"
)

const (
	aliceSteamID = "76561197960287930"
	otherSteamID = "76561197960287931"
)

type linkEnv struct {
	s     *testutil.Server
	steam *steam.FakeSteam
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	s := testutil.NewServer(t)
	fs := steam.NewFakeSteam(t)
	s.Deps.SyncAdapters = sync.NewRegistry(fs.Adapter(nil))
	fs.Players[otherSteamID] = steam.PlayerSummary{SteamID: otherSteamID, PersonaName: "Second", CommunityVisibilityState: 3}
	return &linkEnv{s: s, steam: fs}
}

// startLink follows "Link Steam account" and returns the return_to Steam was given.
func (e *linkEnv) startLink(t *testing.T, c *testutil.Client) string {
	t.Helper()
	res := c.Get("/connections/steam/link")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("link: status %d", res.StatusCode)
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != e.steam.URL+"/openid/login" || q.Get("openid.mode") != "checkid_setup" ||
		q.Get("openid.realm") != "http://openshelf.test" {
		t.Fatalf("redirected to %s", u)
	}
	returnTo := q.Get("openid.return_to")
	if !strings.HasPrefix(returnTo, "http://openshelf.test/connections/steam/callback?state=") {
		t.Fatalf("return_to %s", returnTo)
	}
	return returnTo
}

// callback replays Steam's redirect back to return_to with the assertion appended.
func callback(t *testing.T, c *testutil.Client, returnTo string, assertion url.Values) *testutil.Response {
	t.Helper()
	u, err := url.Parse(returnTo)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, v := range assertion {
		q[k] = v
	}
	return c.Get(u.Path + "?" + q.Encode())
}

func (e *linkEnv) link(t *testing.T, c *testutil.Client, steamID, nonce string) *testutil.Response {
	t.Helper()
	rt := e.startLink(t, c)
	return callback(t, c, rt, e.steam.Assertion(rt, steamID, steam.Nonce(time.Now(), nonce)))
}

func accounts(t *testing.T, s *testutil.Server, userID int64) []db.ConnectedAccount {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(),
		`select id, external_account_id, display_name, coalesce(profile_url, ''), coalesce(avatar_url, ''), credentials is null
		 from connected_accounts where user_id = $1 and provider = 'steam' order by id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.ConnectedAccount
	for rows.Next() {
		var a db.ConnectedAccount
		var profile, avatar string
		var noCreds bool
		if err := rows.Scan(&a.ID, &a.ExternalAccountID, &a.DisplayName, &profile, &avatar, &noCreds); err != nil {
			t.Fatal(err)
		}
		if !noCreds {
			t.Error("steam account stored credentials")
		}
		a.ProfileUrl, a.AvatarUrl = &profile, &avatar
		out = append(out, a)
	}
	return out
}

func syncJobs(t *testing.T, s *testutil.Server, accountID int64) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(),
		`select count(*) from river_job where kind = 'sync_account' and (args->>'account_id')::bigint = $1`, accountID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLinkBindsToLoggedInUser(t *testing.T) {
	e := newLinkEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	ac := e.s.LoginAs(t, alice)

	res := e.link(t, ac, aliceSteamID, "first")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/settings/connections" {
		t.Fatalf("callback: status %d location %q\n%s", res.StatusCode, res.Header.Get("Location"), res.Body)
	}
	got := accounts(t, e.s, alice.ID)
	if len(got) != 1 || got[0].ExternalAccountID != aliceSteamID || got[0].DisplayName != "Rabscuttle" ||
		*got[0].ProfileUrl != "https://steamcommunity.com/id/rabscuttle/" || *got[0].AvatarUrl != "https://avatars.steamstatic.com/abc_medium.jpg" {
		t.Fatalf("accounts %+v", got)
	}
	if syncJobs(t, e.s, got[0].ID) != 1 {
		t.Error("linking didn't enqueue a sync")
	}
	page := ac.Get("/settings/connections").Body
	if !strings.Contains(page, "Linked Steam account Rabscuttle") || !strings.Contains(page, aliceSteamID) {
		t.Error("connections page doesn't show the new account")
	}
	if res := ac.Get("/"); res.StatusCode != http.StatusOK {
		t.Errorf("still logged in as alice: status %d", res.StatusCode)
	}

	// Re-linking refreshes the profile and doesn't duplicate the account.
	p := e.steam.Players[aliceSteamID]
	p.PersonaName, p.AvatarMedium = "Rabscuttle II", "https://avatars.steamstatic.com/new_medium.jpg"
	e.steam.Players[aliceSteamID] = p
	if res := e.link(t, ac, aliceSteamID, "relink"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("relink: status %d\n%s", res.StatusCode, res.Body)
	}
	got = accounts(t, e.s, alice.ID)
	if len(got) != 1 || got[0].DisplayName != "Rabscuttle II" || *got[0].AvatarUrl != "https://avatars.steamstatic.com/new_medium.jpg" {
		t.Fatalf("after relink %+v", got)
	}

	// A user may link several Steam accounts.
	if res := e.link(t, ac, otherSteamID, "second"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("second account: status %d\n%s", res.StatusCode, res.Body)
	}
	if got = accounts(t, e.s, alice.ID); len(got) != 2 || got[1].ExternalAccountID != otherSteamID {
		t.Fatalf("two accounts %+v", got)
	}
}

func TestLinkRefusesSteamIDOfAnotherUser(t *testing.T) {
	e := newLinkEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	if res := e.link(t, e.s.LoginAs(t, alice), aliceSteamID, "a"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("alice: status %d", res.StatusCode)
	}
	res := e.link(t, e.s.LoginAs(t, bob), aliceSteamID, "b")
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.Body, "linked to another OpenShelf user") {
		t.Fatalf("bob: status %d\n%s", res.StatusCode, res.Body)
	}
	if len(accounts(t, e.s, bob.ID)) != 0 {
		t.Fatal("bob got alice's Steam account")
	}
	if got := accounts(t, e.s, alice.ID); len(got) != 1 || got[0].DisplayName != "Rabscuttle" {
		t.Fatalf("alice's account changed: %+v", got)
	}
}

func TestCallbackRefusesBadAssertions(t *testing.T) {
	e := newLinkEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	ac := e.s.LoginAs(t, alice)
	now := time.Now()

	refused := func(name string, res *testutil.Response) {
		t.Helper()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, res.StatusCode)
		}
		if n := len(accounts(t, e.s, alice.ID)); n != 0 {
			t.Fatalf("%s: linked %d accounts", name, n)
		}
	}

	rt := e.startLink(t, ac)
	v := e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "tamper-id"))
	v.Set("openid.claimed_id", "https://steamcommunity.com/openid/id/"+otherSteamID)
	v.Set("openid.identity", "https://steamcommunity.com/openid/id/"+otherSteamID)
	refused("tampered claimed_id", callback(t, ac, rt, v))

	rt = e.startLink(t, ac)
	v = e.steam.Assertion(strings.Replace(rt, "openshelf.test", "evil.test", 1), aliceSteamID, steam.Nonce(now, "tamper-rt"))
	refused("tampered return_to", callback(t, ac, rt, v))

	rt = e.startLink(t, ac)
	e.startLink(t, ac)
	refused("stale state", callback(t, ac, rt, e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "stale"))))

	rt = e.startLink(t, ac)
	bad := strings.Replace(rt, "state=", "state=x", 1)
	refused("bad state", callback(t, ac, bad, e.steam.Assertion(bad, aliceSteamID, steam.Nonce(now, "bad-state"))))

	refused("no session state", callback(t, ac, rt, e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "no-state"))))

	e.steam.Invalid = true
	rt = e.startLink(t, ac)
	refused("is_valid:false", callback(t, ac, rt, e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "invalid"))))
	e.steam.Invalid = false

	rt = e.startLink(t, ac)
	if res := callback(t, ac, rt, e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "once"))); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid: status %d\n%s", res.StatusCode, res.Body)
	}
	if _, err := e.s.Pool.Exec(context.Background(), `delete from connected_accounts`); err != nil {
		t.Fatal(err)
	}
	rt = e.startLink(t, ac)
	res := callback(t, ac, rt, e.steam.Assertion(rt, aliceSteamID, steam.Nonce(now, "once")))
	refused("replayed nonce", res)
	if !strings.Contains(res.Body, "already used") {
		t.Errorf("replay message missing:\n%s", res.Body)
	}
}

func TestCallbackCancelAndAvailability(t *testing.T) {
	e := newLinkEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	ac := e.s.LoginAs(t, alice)

	rt := e.startLink(t, ac)
	res := callback(t, ac, rt, url.Values{"openid.ns": {"http://specs.openid.net/auth/2.0"}, "openid.mode": {"cancel"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(ac.Get("/settings/connections").Body, "cancelled") {
		t.Fatalf("cancel: status %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, e.s.URL+"/connections/steam/link", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Boosted", "true")
	if res := ac.Do(req); res.StatusCode != http.StatusNoContent || !strings.HasPrefix(res.Header.Get("HX-Redirect"), e.steam.URL+"/openid/login?") {
		t.Fatalf("boosted link: status %d HX-Redirect %q", res.StatusCode, res.Header.Get("HX-Redirect"))
	}

	if res := e.s.Client(t).Get("/connections/steam/link"); res.StatusCode != http.StatusSeeOther || strings.Contains(res.Header.Get("Location"), "openid") {
		t.Fatalf("anonymous link: status %d location %s", res.StatusCode, res.Header.Get("Location"))
	}

	e.s.Deps.SyncAdapters = sync.NewRegistry(steam.New(testutil.TestConfig()))
	if res := ac.Get("/connections/steam/link"); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/settings/connections" {
		t.Fatalf("unconfigured link: status %d location %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res := ac.Get("/connections/steam/callback?state=x"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured callback: status %d", res.StatusCode)
	}
}
