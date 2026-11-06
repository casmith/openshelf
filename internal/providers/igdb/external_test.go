package igdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/config"
)

func TestGamesBySteamApp(t *testing.T) {
	var bodies []string
	uidList := regexp.MustCompile(`where uid = \(([^)]*)\) & external_game_source = 1; limit 500;`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":5000000}`))
		case "/v4/external_games":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			m := uidList.FindStringSubmatch(string(b))
			if m == nil || !strings.HasPrefix(string(b), "fields game,uid;") {
				http.Error(w, "bad query "+string(b), http.StatusBadRequest)
				return
			}
			var rows []map[string]any
			for _, q := range strings.Split(m[1], ",") {
				uid := strings.Trim(q, `"`)
				switch uid {
				case "620":
					rows = append(rows, map[string]any{"id": 1, "game": 72, "uid": "620"}, map[string]any{"id": 2, "game": 999, "uid": "620"})
				case "400":
					rows = append(rows, map[string]any{"id": 3, "game": 71, "uid": "400"})
				case "1000":
					rows = append(rows, map[string]any{"id": 4, "game": 5000, "uid": "1000"})
				}
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := New(config.Config{IGDBClientID: "cid", IGDBClientSecret: "secret"})
	p.APIURL, p.TokenURL, p.HTTP.Limiter = srv.URL+"/v4", srv.URL+"/token", nil

	ids := []string{"620", "400", "620", "bad\"id", "12"}
	for i := 0; i < 600; i++ {
		ids = append(ids, fmt.Sprint(100000+i))
	}
	ids = append(ids, "1000")
	got, err := p.GamesBySteamApp(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"620": "72", "400": "71", "1000": "5000"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s -> %q, want %q", k, got[k], v)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("%d queries, want 2 batches", len(bodies))
	}
	if strings.Contains(bodies[0], "bad") {
		t.Error("non-numeric appid sent to IGDB")
	}

	if _, err := New(config.Config{}).GamesBySteamApp(context.Background(), []string{"620"}); err == nil {
		t.Error("unconfigured provider looked up appids")
	}
}
