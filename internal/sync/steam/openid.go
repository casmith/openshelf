package steam

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	openIDNS         = "http://specs.openid.net/auth/2.0"
	identifierSelect = "http://specs.openid.net/auth/2.0/identifier_select"
	// NonceMaxAge bounds how old a response_nonce may be; used nonces are kept at least this long.
	NonceMaxAge = time.Hour
)

var (
	ErrOpenIDInvalid = errors.New("steam: the Steam sign-in response couldn't be verified")
	claimedIDPattern = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(\d{17})$`)
	// OpenID 2.0 section 10.1: these must be covered by the signature.
	requiredSigned = []string{"op_endpoint", "return_to", "response_nonce", "assoc_handle", "claimed_id", "identity"}
)

// LoginURL is the Steam OpenID checkid_setup URL that returns to returnTo.
func (a *Adapter) LoginURL(returnTo, realm string) string {
	q := url.Values{
		"openid.ns":         {openIDNS},
		"openid.mode":       {"checkid_setup"},
		"openid.claimed_id": {identifierSelect},
		"openid.identity":   {identifierSelect},
		"openid.return_to":  {returnTo},
		"openid.realm":      {realm},
	}
	return a.OpenIDEndpoint + "?" + q.Encode()
}

// VerifyAssertion checks a Steam OpenID 2.0 positive assertion served at returnTo:
// local checks first, then check_authentication with Steam. It returns the
// SteamID64 and the response nonce, which the caller must reject if reused.
func (a *Adapter) VerifyAssertion(ctx context.Context, params url.Values, returnTo string, now time.Time) (steamID, nonce string, err error) {
	fail := func(why string) (string, string, error) {
		return "", "", fmt.Errorf("%w: %s", ErrOpenIDInvalid, why)
	}
	if len(params["openid.mode"]) != 1 || params.Get("openid.mode") != "id_res" {
		return fail("not a positive assertion")
	}
	if params.Get("openid.ns") != openIDNS {
		return fail("wrong namespace")
	}
	if params.Get("openid.op_endpoint") != a.OpenIDEndpoint {
		return fail("unexpected OpenID provider")
	}
	if params.Get("openid.return_to") != returnTo {
		return fail("return_to mismatch")
	}
	claimed := params.Get("openid.claimed_id")
	m := claimedIDPattern.FindStringSubmatch(claimed)
	if m == nil || params.Get("openid.identity") != claimed {
		return fail("unexpected claimed_id")
	}
	signed := map[string]bool{}
	for _, f := range strings.Split(params.Get("openid.signed"), ",") {
		signed[f] = true
	}
	for _, f := range requiredSigned {
		if !signed[f] {
			return fail(f + " is not signed")
		}
	}
	nonce = params.Get("openid.response_nonce")
	if len(nonce) < 20 {
		return fail("missing nonce")
	}
	ts, perr := time.Parse(time.RFC3339, nonce[:20])
	if perr != nil || ts.Before(now.Add(-NonceMaxAge)) || ts.After(now.Add(5*time.Minute)) {
		return fail("stale nonce")
	}
	for k, vs := range params {
		if strings.HasPrefix(k, "openid.") && len(vs) != 1 {
			return fail("repeated parameter " + k)
		}
	}

	ok, err := a.checkAuthentication(ctx, params)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return fail("Steam rejected the assertion")
	}
	return m[1], nonce, nil
}

func (a *Adapter) checkAuthentication(ctx context.Context, params url.Values) (bool, error) {
	form := url.Values{}
	for k, vs := range params {
		if strings.HasPrefix(k, "openid.") {
			form[k] = vs
		}
	}
	form.Set("openid.mode", "check_authentication")
	req, err := http.NewRequest(http.MethodPost, a.OpenIDEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.Client.HTTP.Do(ctx, req)
	if err != nil {
		return false, fmt.Errorf("steam: check_authentication: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("steam: check_authentication: HTTP %d", res.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	valid, ns := false, false
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		switch k {
		case "ns":
			ns = v == openIDNS
		case "is_valid":
			valid = v == "true"
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return valid && ns, nil
}
