package steam

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/config"
)

const testReturnTo = "http://openshelf.test/connections/steam/callback?state=abc"

func TestLoginURL(t *testing.T) {
	a := New(config.Config{SteamAPIKey: "k"})
	u, err := url.Parse(a.LoginURL(testReturnTo, "http://openshelf.test"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != "https://steamcommunity.com/openid/login" ||
		q.Get("openid.ns") != "http://specs.openid.net/auth/2.0" || q.Get("openid.mode") != "checkid_setup" ||
		q.Get("openid.claimed_id") != identifierSelect || q.Get("openid.identity") != identifierSelect ||
		q.Get("openid.return_to") != testReturnTo || q.Get("openid.realm") != "http://openshelf.test" {
		t.Fatalf("login URL %s", u)
	}
}

func TestVerifyAssertion(t *testing.T) {
	fs := NewFakeSteam(t)
	a := fs.Adapter(nil)
	ctx := context.Background()
	now := time.Now()
	fresh := func() url.Values { return fs.Assertion(testReturnTo, testSteamID, Nonce(now, "n1")) }

	id, nonce, err := a.VerifyAssertion(ctx, fresh(), testReturnTo, now)
	if err != nil || id != testSteamID || nonce != Nonce(now, "n1") {
		t.Fatalf("valid assertion: %q %q %v", id, nonce, err)
	}

	cases := map[string]func(v url.Values){
		"tampered claimed_id": func(v url.Values) {
			v.Set("openid.claimed_id", "https://steamcommunity.com/openid/id/76561197960287931")
			v.Set("openid.identity", "https://steamcommunity.com/openid/id/76561197960287931")
		},
		"identity differs":   func(v url.Values) { v.Set("openid.identity", "https://steamcommunity.com/openid/id/76561197960287931") },
		"tampered return_to": func(v url.Values) { v.Set("openid.return_to", "http://evil.test/connections/steam/callback?state=abc") },
		"signed return_to":   func(v url.Values) { v.Set("openid.return_to", "http://evil.test/cb"); fs.Resign(v) },
		"other op_endpoint":  func(v url.Values) { v.Set("openid.op_endpoint", "https://evil.test/openid/login"); fs.Resign(v) },
		"bad claimed_id form": func(v url.Values) {
			v.Set("openid.claimed_id", "https://evil.test/openid/id/76561197960287930")
			fs.Resign(v)
		},
		"unsigned claimed_id": func(v url.Values) {
			v.Set("openid.signed", "signed,op_endpoint,return_to,response_nonce,assoc_handle,identity")
			fs.Resign(v)
		},
		"cancel":       func(v url.Values) { v.Set("openid.mode", "cancel") },
		"setup needed": func(v url.Values) { v.Set("openid.mode", "setup_needed") },
		"stale nonce": func(v url.Values) {
			v.Set("openid.response_nonce", Nonce(now.Add(-2*time.Hour), "n2"))
			fs.Resign(v)
		},
		"garbage nonce": func(v url.Values) { v.Set("openid.response_nonce", "not-a-nonce-at-all-really"); fs.Resign(v) },
		"repeated param": func(v url.Values) {
			v.Add("openid.claimed_id", "https://steamcommunity.com/openid/id/76561197960287931")
		},
		"forged sig":      func(v url.Values) { v.Set("openid.sig", "AAAA") },
		"wrong namespace": func(v url.Values) { v.Set("openid.ns", "http://openid.net/signon/1.1") },
	}
	for name, mutate := range cases {
		v := fresh()
		mutate(v)
		if _, _, err := a.VerifyAssertion(ctx, v, testReturnTo, now); !errors.Is(err, ErrOpenIDInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	before := fs.Count("/openid/login")
	v := fresh()
	v.Set("openid.return_to", "http://evil.test/")
	_, _, _ = a.VerifyAssertion(ctx, v, testReturnTo, now)
	if fs.Count("/openid/login") != before {
		t.Error("contacted Steam for an assertion that fails local checks")
	}

	fs.Invalid = true
	if _, _, err := a.VerifyAssertion(ctx, fresh(), testReturnTo, now); !errors.Is(err, ErrOpenIDInvalid) {
		t.Errorf("is_valid:false: err = %v", err)
	}
	fs.Invalid = false

	fs.Close()
	if _, _, err := a.VerifyAssertion(ctx, fresh(), testReturnTo, now); err == nil || errors.Is(err, ErrOpenIDInvalid) || !strings.Contains(err.Error(), "check_authentication") {
		t.Errorf("unreachable OP: err = %v", err)
	}
}
