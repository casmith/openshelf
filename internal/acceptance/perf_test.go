package acceptance_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/testutil"
)

func TestPageBudgetsWith2000Entries(t *testing.T) {
	s := testutil.NewServer(t)
	alice := testutil.CreateUser(t, s.Q, "alice")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into media_items (kind, title, sort_title, genres, release_year)
		select (array['game','book','film','show']::media_kind[])[1 + g % 4], 'Item ' || g, 'Item ' || lpad(g::text, 5, '0'),
		       array[(array['drama','comedy','rpg','sci-fi'])[1 + g % 4]], 1980 + g % 40
		from generate_series(1, 2000) g`)
	exec(`insert into editions (media_item_id, platform_id)
		select id, (select id from platforms order by id limit 1) from media_items where kind = 'game'`)
	for _, sql := range []string{
		`insert into library_entries (user_id, media_item_id, ownership_user, backlog, tags)
		 select $1, id, 'owned', id % 5 = 0, '{fav}' from media_items`,
		`insert into experiences (user_id, media_item_id, state, started_on, finished_on, progress_unit, progress_current)
		 select $1, id, (case when id % 3 = 0 then 'finished' else 'active' end)::experience_state,
		        current_date - (id % 300)::int, case when id % 3 = 0 then current_date - (id % 200)::int end, 'hours', 3
		 from media_items where id % 2 = 0`,
		`insert into reviews (user_id, media_item_id, rating, body) select $1, id, 20 + id % 80, 'ok' from media_items where id % 4 = 0`,
		`insert into activity_events (user_id, verb, media_item_id, library_entry_id, occurred_at)
		 select $1, 'added', e.media_item_id, e.id, now() - (e.id % 400) * interval '1 day' from library_entries e where e.user_id = $1`,
		`insert into activity_events (user_id, verb, media_item_id, experience_id, occurred_at)
		 select $1, 'finished', x.media_item_id, x.id, x.finished_on from experiences x where x.state = 'finished' and x.user_id = $1`,
		`insert into activity_events (user_id, verb, media_item_id, review_id, occurred_at)
		 select $1, 'rated', r.media_item_id, r.id, now() - (r.id % 100) * interval '1 day' from reviews r where r.user_id = $1`,
	} {
		exec(sql, alice.ID)
	}
	exec(`analyze`)
	c := s.LoginAs(t, alice)

	for _, tc := range []struct {
		path       string
		maxQueries int
	}{
		{"/", 9},
		{"/library", 5},
		{"/library?view=table", 5},
		{"/journal", 4},
	} {
		c.Get(tc.path) // warm caches and settle the session write from a view toggle
		var res *testutil.Response
		var elapsed time.Duration
		n := testutil.CountQueries(t, s.Pool, func() {
			start := time.Now()
			res = c.Get(tc.path)
			elapsed = time.Since(start)
		})
		want(t, res, http.StatusOK)
		t.Logf("%s: %d queries, %s", tc.path, n, elapsed)
		if n > tc.maxQueries {
			t.Errorf("%s: %d queries, want <= %d", tc.path, n, tc.maxQueries)
		}
		if elapsed > 200*time.Millisecond {
			t.Errorf("%s: took %s, want < 200ms", tc.path, elapsed)
		}
	}
}
