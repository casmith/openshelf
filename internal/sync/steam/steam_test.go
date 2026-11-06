package steam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync/synctest"
)

func TestConfigured(t *testing.T) {
	if New(config.Config{}).Configured() {
		t.Fatal("configured without an API key")
	}
	a := New(config.Config{SteamAPIKey: "k"})
	if !a.Configured() || a.Provider() != "steam" || a.Capabilities().LinkPath != "/connections/steam/link" {
		t.Fatalf("adapter %+v", a.Capabilities())
	}
}

func TestClientGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IPlayerService/GetOwnedGames/v1/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("key") != "secret" || q.Get("steamid") != "765" || q.Get("format") != "json" {
			http.Error(w, "bad query", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"game_count":1}}`))
	}))
	defer srv.Close()
	c := NewClient("secret")
	c.BaseURL = srv.URL
	var out struct {
		Response struct {
			GameCount int `json:"game_count"`
		} `json:"response"`
	}
	if err := c.Get(context.Background(), "IPlayerService", "GetOwnedGames", "v1", url.Values{"steamid": {"765"}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Response.GameCount != 1 {
		t.Fatalf("decoded %+v", out)
	}
}

func TestSyncRoundTripsCursor(t *testing.T) {
	a := New(config.Config{SteamAPIKey: "k"})
	in, _ := json.Marshal(Cursor{Owned: json.RawMessage(`{"x":1}`)})
	out, err := a.Sync(context.Background(), db.ConnectedAccount{}, in, &synctest.Recorder{})
	if err != nil {
		t.Fatal(err)
	}
	var cur Cursor
	if err := json.Unmarshal(out, &cur); err != nil || string(cur.Owned) != `{"x":1}` {
		t.Fatalf("cursor %s: %v", out, err)
	}
}
