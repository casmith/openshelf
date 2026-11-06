package steam

import (
	"context"
	"net/url"
	"strings"

	"github.com/casmith/openshelf/internal/providers"
)

const DefaultBaseURL = "https://api.steampowered.com"

// Client calls the Steam Web API. Tests point BaseURL at an httptest server.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *providers.HTTP
}

func NewClient(key string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Key: key, HTTP: providers.NewHTTP(4, 4)}
}

// Get calls /{iface}/{method}/{version}/ with the API key and params and decodes JSON into out,
// e.g. c.Get(ctx, "IPlayerService", "GetOwnedGames", "v1", params, &resp).
// Errors are *providers.StatusError (401/403 for a bad key or private data) or providers.ErrNotFound.
func (c *Client) Get(ctx context.Context, iface, method, version string, params url.Values, out any) error {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("key", c.Key)
	q.Set("format", "json")
	u := strings.TrimRight(c.BaseURL, "/") + "/" + iface + "/" + method + "/" + version + "/?" + q.Encode()
	return c.HTTP.GetJSON(ctx, u, nil, out)
}
