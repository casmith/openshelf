package synchistory_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/testutil"
)

func TestRunCountsNameWhatTheProviderSyncs(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	c := s.LoginAs(t, alice)
	for _, tc := range []struct{ provider, counts, want, not string }{
		{"steam", `{"games": 2, "entries_created": 1}`, "2 games · 1 added", "books"},
		{"audiobookshelf", `{"games": 3}`, "3 books", "games"},
		{"audiobookshelf", `{"games": 1}`, "1 book", "games"},
	} {
		acct := testutil.CreateConnectedAccount(t, s.Pool, alice.ID, tc.provider, tc.provider+"-"+tc.counts)
		if _, err := s.Pool.Exec(context.Background(), `insert into sync_runs (account_id, status, counts, finished_at) values ($1, 'succeeded', $2::jsonb, now())`, acct.ID, tc.counts); err != nil {
			t.Fatal(err)
		}
		res := c.Get("/settings/connections/" + acct.PublicID.String() + "/history")
		want(t, res, http.StatusOK)
		body := text(res.Body)
		contains(t, body, tc.want)
		if strings.Contains(body, " "+tc.not) {
			t.Errorf("%s history says %q:\n%s", tc.provider, tc.not, body)
		}
	}
}
