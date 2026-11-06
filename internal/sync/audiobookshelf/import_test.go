package audiobookshelf_test

import (
	"context"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/sync/runner"
	"github.com/casmith/openshelf/internal/testutil"
)

func run(t *testing.T, e *env, acct db.ConnectedAccount) runner.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), e.s.Deps, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func itemFor(t *testing.T, e *env, key string) int64 {
	t.Helper()
	return scalar[int64](t, e.s, `select media_item_id from external_ids where provider = 'audiobookshelf_item' and value = $1`, key)
}

func events(t *testing.T, e *env, acct db.ConnectedAccount) map[string]int {
	t.Helper()
	rows, err := e.s.Pool.Query(context.Background(), `select kind, count(*) from sync_events where account_id = $1 group by kind`, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		out[k] = n
	}
	return out
}

func TestImportWholeLibrary(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)

	res := run(t, e, acct)
	if res.Err != nil || res.Status != db.SyncRunStatusPartial {
		t.Fatalf("run: status %s err %v warnings %v", res.Status, res.Err, res.Warnings)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no longer in Audiobookshelf") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if c := res.Counts; c.Games != 3 || c.WithProgress != 2 || c.EntriesCreated != 3 || c.ItemsResolved != 1 || c.ItemsCreated != 2 || c.Deferred != 0 {
		t.Fatalf("counts %+v", res.Counts)
	}

	// Hail Mary resolved to the Open Library work by ISBN.
	hm := itemFor(t, e, itemHailMary)
	if got := scalar[string](t, e.s, `select metadata_provider from media_items where id = $1`, hm); got != "openlibrary" {
		t.Errorf("hail mary provider %q", got)
	}
	if got := scalar[int64](t, e.s, `select media_item_id from external_ids where provider = 'openlibrary_work' and value = 'OL1W'`); got != hm {
		t.Errorf("hail mary is item %d, OL1W is %d", hm, got)
	}

	// The Way of Kings had no exact match: a manual book with credits and an audiobook edition.
	wk := itemFor(t, e, itemKings)
	if got := scalar[string](t, e.s, `select kind::text || '|' || title || '|' || coalesce(release_year, 0) from media_items where id = $1`, wk); got != "book|The Way of Kings|2010" {
		t.Errorf("manual item %q", got)
	}
	if got := scalar[string](t, e.s, `select series_name || ' #' || series_position from book_details where media_item_id = $1`, wk); got != "The Stormlight Archive #1.00" {
		t.Errorf("series %q", got)
	}
	if got := scalar[string](t, e.s, `select string_agg(c.role || ':' || cr.name, ', ' order by c.role, c.position) from credits c join creators cr on cr.id = c.creator_id where c.media_item_id = $1`, wk); got != "author:Brandon Sanderson, narrator:Michael Kramer, narrator:Kate Reading" {
		t.Errorf("credits %q", got)
	}
	if got := scalar[string](t, e.s, `select format::text || ' ' || duration_minutes from editions where media_item_id = $1`, wk); got != "audiobook 2760" {
		t.Errorf("edition %q", got)
	}
	if n := scalar[int](t, e.s, `select count(*) from media_items`); n != 3 {
		t.Errorf("%d items; podcasts must not be imported", n)
	}
	// The untouched book is owned, not played, not in the backlog, and has no progress.
	un := itemFor(t, e, itemUnread)
	if got := scalar[string](t, e.s, `select ownership_source::text || ' ' || (played_source is null) || ' ' || backlog || ' ' || visibility::text
		|| ' ' || (ownership_user is null and played_user is null) from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, un); got != "owned true false private true" {
		t.Errorf("untouched entry %q", got)
	}
	if n := scalar[int](t, e.s, `select count(*) from external_progress where media_item_id = $1`, un); n != 0 {
		t.Errorf("untouched book has %d progress rows", n)
	}

	for _, id := range []int64{hm, wk} {
		got := scalar[string](t, e.s, `select ownership_source::text || ' ' || played_source || ' ' || backlog || ' ' || visibility::text
			|| ' ' || (ownership_user is null and played_user is null) from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, id)
		if got != "owned true false private true" {
			t.Errorf("entry for %d: %q", id, got)
		}
	}

	got := scalar[string](t, e.s, `select round(fraction::numeric, 4) || ' ' || current_seconds || ' ' || duration_seconds || ' ' || finished || ' ' || (last_activity_at = $2)
		from external_progress where account_id = $1 and provider_item_key = 'li_hailmary'`, acct.ID, lastListened)
	if got != "0.4333 18720 43201 false true" {
		t.Errorf("hail mary progress %q", got)
	}
	got = scalar[string](t, e.s, `select fraction || ' ' || finished || ' ' || (finished_at = $2) from external_progress where account_id = $1 and provider_item_key = 'li_kings'`, acct.ID, finishedAt)
	if got != "1 true true" {
		t.Errorf("kings progress %q", got)
	}
	ev := events(t, e, acct)
	if ev[sync.EventEntryCreated] != 3 || ev[sync.EventProgressChanged] != 2 || ev[sync.EventSourceFinished] != 1 || ev[sync.EventNeedsReview] != 2 {
		t.Errorf("events %v", ev)
	}
	if got := scalar[string](t, e.s, `select after from sync_events where kind = 'source_finished'`); got != "2026-01-15" {
		t.Errorf("finished event %q", got)
	}
	if n := scalar[int](t, e.s, `select (select count(*) from experiences) + (select count(*) from activity_events)`); n != 0 {
		t.Errorf("sync created %d experiences or activity events", n)
	}

	// Re-running changes nothing.
	before := events(t, e, acct)
	res = run(t, e, acct)
	if res.Err != nil || res.Counts.EntriesCreated != 0 || res.Counts.EntriesUpdated != 0 || res.Counts.ItemsCreated != 0 {
		t.Fatalf("re-run: %+v %v", res.Counts, res.Err)
	}
	if after := events(t, e, acct); after[sync.EventProgressChanged] != before[sync.EventProgressChanged] || after[sync.EventSourceFinished] != before[sync.EventSourceFinished] || after[sync.EventEntryCreated] != 3 || len(after) != len(before) {
		t.Errorf("re-run events %v, before %v", after, before)
	}
	if n := scalar[int](t, e.s, `select count(*) from media_items`); n != 3 {
		t.Errorf("re-run created items: %d", n)
	}
	if lookups := e.ol.lookups(); run(t, e, acct).Err != nil || e.ol.lookups() != lookups {
		t.Errorf("re-run looked up imported books: %d -> %d", lookups, e.ol.lookups())
	}

	// More listening, then finishing, each record one event.
	e.abs.setUser(aliceKey, meBody(aliceABSID, "alice_abs", aliceProgress(0.6, 25920, false)))
	run(t, e, acct)
	if got := scalar[string](t, e.s, `select before || ' → ' || after from sync_events where kind = 'progress_changed' and provider_item_key = 'li_hailmary' order by id desc limit 1`); got != "0.4333 → 0.6000" {
		t.Errorf("progress event %q", got)
	}
	e.abs.setUser(aliceKey, meBody(aliceABSID, "alice_abs", aliceProgress(1, 43200, true)))
	run(t, e, acct)
	run(t, e, acct)
	ev = events(t, e, acct)
	if ev[sync.EventProgressChanged] != 4 || ev[sync.EventSourceFinished] != 2 {
		t.Errorf("events after finishing %v", ev)
	}
}

func TestPlayedOverrideRespected(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	run(t, e, acct)
	hm := itemFor(t, e, itemHailMary)
	if _, err := e.s.Pool.Exec(context.Background(), `update library_entries set played_user = false where user_id = $1 and media_item_id = $2`, alice.ID, hm); err != nil {
		t.Fatal(err)
	}
	e.abs.setUser(aliceKey, meBody(aliceABSID, "alice_abs", aliceProgress(0.7, 30240, false)))
	run(t, e, acct)
	if got := scalar[bool](t, e.s, `select played_user from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, hm); got {
		t.Error("sync changed played_user")
	}
	if n := scalar[int](t, e.s, `select count(*) from sync_events where kind = 'override_respected' and respected_override and media_item_id = $1`, hm); n != 1 {
		t.Errorf("%d override_respected events", n)
	}
}

func TestUnauthorizedKeyMarksAccountError(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, "revoked-key")
	res := run(t, e, acct)
	if res.Status != db.SyncRunStatusFailed || res.Err == nil {
		t.Fatalf("status %s err %v", res.Status, res.Err)
	}
	got := scalar[string](t, e.s, `select status::text || ': ' || last_error from connected_accounts where id = $1`, acct.ID)
	if !strings.HasPrefix(got, "error: Audiobookshelf rejected the API key") || strings.Contains(got, "revoked-key") {
		t.Errorf("account %q", got)
	}

	e.abs.Status = 403
	acct2 := e.linkAccount(t, alice, "other-abs-user", aliceKey)
	if res := run(t, e, acct2); res.Err == nil || !strings.Contains(res.Err.Error(), "rejected") {
		t.Errorf("403: %v", res.Err)
	}
}

func TestCrossUserIsolation(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	bob := testutil.CreateUser(t, e.s.Q, "bob")
	aliceAcct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	bobAcct := e.linkAccount(t, bob, bobABSID, bobKey)
	run(t, e, aliceAcct)
	run(t, e, bobAcct)
	// Bob's key sees the same shared library, so he owns the same books, without alice's progress.
	if got := scalar[string](t, e.s, `select count(*) || ' ' || count(played_source) from library_entries where user_id = $1`, bob.ID); got != "3 0" {
		t.Errorf("bob's entries (count, with played_source): %s", got)
	}
	if n := scalar[int](t, e.s, `select count(*) from external_progress where account_id = $1`, bobAcct.ID); n != 0 {
		t.Errorf("bob has %d progress rows", n)
	}

	hm := scalar[string](t, e.s, `select public_id::text from media_items where id = $1`, itemFor(t, e, itemHailMary))
	page := text(e.s.LoginAs(t, bob).Get("/items/" + hm).Body)
	if strings.Contains(page, "Synced sources") || strings.Contains(page, "43%") {
		t.Error("bob sees alice's listening progress")
	}
	accountID := scalar[string](t, e.s, `select public_id::text from connected_accounts where id = $1`, aliceAcct.ID)
	res := e.s.LoginAs(t, bob).PostForm("/connections/"+accountID+"/exclusions", map[string][]string{"key": {itemHailMary}})
	if res.StatusCode != 404 {
		t.Errorf("bob excluding alice's item: %d", res.StatusCode)
	}
}
