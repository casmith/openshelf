package steam

import (
	"context"
	"errors"
	"net/url"
)

var ErrOpenIDNotImplemented = errors.New("steam: OpenID verification is not implemented")

// VerifyAssertion checks a Steam OpenID 2.0 callback (check_authentication) and
// returns the SteamID64 it asserts (S20).
func (a *Adapter) VerifyAssertion(ctx context.Context, params url.Values) (string, error) {
	return "", ErrOpenIDNotImplemented
}
