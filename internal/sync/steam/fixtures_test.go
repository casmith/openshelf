package steam

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/igdb"
)

// FakeSteam is an httptest Steam Web API plus OpenID provider serving testdata fixtures.
type FakeSteam struct {
	*httptest.Server
	t       testing.TB
	secret  []byte
	mu      stdsync.Mutex
	Key     string
	Players map[string]PlayerSummary
	Owned   map[string]string // steamid -> response body; missing means the owned_games.json fixture
	Invalid bool              // check_authentication answers is_valid:false
	Hits    map[string]int    // request path -> count
}

func NewFakeSteam(t testing.TB) *FakeSteam {
	t.Helper()
	f := &FakeSteam{t: t, secret: []byte("op-secret"), Key: "steam-key", Owned: map[string]string{}, Hits: map[string]int{}}
	var summaries struct {
		Response struct {
			Players []PlayerSummary `json:"players"`
		} `json:"response"`
	}
	if err := json.Unmarshal(readFixture(t, "player_summaries.json"), &summaries); err != nil {
		t.Fatal(err)
	}
	f.Players = map[string]PlayerSummary{}
	for _, p := range summaries.Response.Players {
		f.Players[p.SteamID] = p
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Adapter returns a configured adapter that talks only to the fake.
func (f *FakeSteam) Adapter(metadata *providers.Registry) *Adapter {
	a := New(config.Config{SteamAPIKey: f.Key})
	a.Client.BaseURL = f.URL
	a.Client.HTTP.Limiter = nil
	a.OpenIDEndpoint = f.URL + "/openid/login"
	a.Metadata = metadata
	return a
}

func (f *FakeSteam) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Hits[r.URL.Path]++
	if r.URL.Path == "/openid/login" {
		f.checkAuthentication(w, r)
		return
	}
	q := r.URL.Query()
	if q.Get("key") != f.Key {
		http.Error(w, "<html><body>Forbidden</body></html>", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/ISteamUser/GetPlayerSummaries/v2/":
		players := []PlayerSummary{}
		if p, ok := f.Players[q.Get("steamids")]; ok {
			players = append(players, p)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"players": players}})
	case "/IPlayerService/GetOwnedGames/v1/":
		if q.Get("include_appinfo") != "1" || q.Get("include_played_free_games") != "1" {
			http.Error(w, "missing flags", http.StatusBadRequest)
			return
		}
		if body, ok := f.Owned[q.Get("steamid")]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write(readFixture(f.t, "owned_games.json"))
	default:
		http.NotFound(w, r)
	}
}

func (f *FakeSteam) checkAuthentication(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "checkid_setup is interactive", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	valid := r.PostForm.Get("openid.mode") == "check_authentication" &&
		r.PostForm.Get("openid.assoc_handle") == "1234567890" &&
		hmac.Equal([]byte(r.PostForm.Get("openid.sig")), []byte(f.sign(r.PostForm))) && !f.Invalid
	_, _ = fmt.Fprintf(w, "ns:%s\nis_valid:%t\n", openIDNS, valid)
}

func (f *FakeSteam) sign(v url.Values) string {
	m := hmac.New(sha256.New, f.secret)
	for _, k := range strings.Split(v.Get("openid.signed"), ",") {
		_, _ = io.WriteString(m, k+":"+v.Get("openid."+k)+"\n")
	}
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Assertion is the id_res response Steam would redirect back with, signed by the fake OP.
func (f *FakeSteam) Assertion(returnTo, steamID, nonce string) url.Values {
	claimed := "https://steamcommunity.com/openid/id/" + steamID
	v := url.Values{
		"openid.ns":             {openIDNS},
		"openid.mode":           {"id_res"},
		"openid.op_endpoint":    {f.URL + "/openid/login"},
		"openid.claimed_id":     {claimed},
		"openid.identity":       {claimed},
		"openid.return_to":      {returnTo},
		"openid.response_nonce": {nonce},
		"openid.assoc_handle":   {"1234567890"},
		"openid.signed":         {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
	}
	v.Set("openid.sig", f.sign(v))
	return v
}

// Resign recomputes the signature after a test changes signed fields as Steam would never do.
func (f *FakeSteam) Resign(v url.Values) { v.Set("openid.sig", f.sign(v)) }

func Nonce(t time.Time, unique string) string {
	return t.UTC().Format("2006-01-02T15:04:05Z") + unique
}

// FakeIGDB serves external_games and games lookups for a configured igdb.Provider.
type FakeIGDB struct {
	*httptest.Server
	mu    stdsync.Mutex
	Fail  bool
	Hits  map[string]int
	Games map[string]string // steam appid -> igdb game id
}

func NewFakeIGDB(t testing.TB) (*FakeIGDB, *igdb.Provider) {
	t.Helper()
	f := &FakeIGDB{Hits: map[string]int{}, Games: map[string]string{"620": "72", "400": "71"}}
	uids := regexp.MustCompile(`where uid = \(([^)]*)\) & external_game_source = 1;`)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Hits[r.URL.Path]++
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":5000000}`))
			return
		}
		if f.Fail {
			http.Error(w, "upstream down", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v4/external_games":
			m := uids.FindStringSubmatch(string(body))
			if m == nil {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			rows := []map[string]any{}
			for _, uid := range strings.Split(m[1], ",") {
				uid = strings.Trim(uid, `"`)
				if g, ok := f.Games[uid]; ok {
					rows = append(rows, map[string]any{"game": json.Number(g), "uid": uid})
				}
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/v4/games":
			if strings.Contains(string(body), "where id = 72;") {
				_, _ = w.Write(readFixture(t, "igdb_game_72.json"))
				return
			}
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	p := igdb.New(config.Config{IGDBClientID: "cid", IGDBClientSecret: "secret"})
	p.APIURL, p.TokenURL, p.HTTP.Limiter = f.URL+"/v4", f.URL+"/token", nil
	return f, p
}

func (f *FakeIGDB) Count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Hits[path]
}

func (f *FakeSteam) Count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Hits[path]
}
