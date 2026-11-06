package audiobookshelf_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/testutil"
)

const itemPandora = "li_pandora"

func exec(t *testing.T, e *env, sql string, args ...any) {
	t.Helper()
	if _, err := e.s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// Earlier syncs created manual items with ABS titles like "X (Unabridged)".
func TestTitleFixOnSyncCreatedManualItem(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	run(t, e, acct)
	wk, un, hm := itemFor(t, e, itemKings), itemFor(t, e, itemUnread), itemFor(t, e, itemHailMary)
	exec(t, e, `update media_items set title = title || ' (Unabridged)', sort_title = 'x' where id = any($1)`, []int64{wk, un, hm})
	exec(t, e, `update media_items set locked_fields = '{title}' where id = $1`, un)

	res := run(t, e, acct)
	if res.Err != nil || res.Counts.ItemsCreated != 0 {
		t.Fatalf("run %+v %v", res.Counts, res.Err)
	}
	if got := scalar[string](t, e.s, `select title || '|' || sort_title from media_items where id = $1`, wk); got != "The Way of Kings|Way of Kings, The" {
		t.Errorf("manual item title %q", got)
	}
	if got := scalar[string](t, e.s, `select title from media_items where id = $1`, un); got != "Unread Book (Unabridged)" {
		t.Errorf("locked title changed to %q", got)
	}
	if got := scalar[string](t, e.s, `select title from media_items where id = $1`, hm); got != "Project Hail Mary (Unabridged)" {
		t.Errorf("provider item title changed to %q", got)
	}
	if got := scalar[string](t, e.s, `select string_agg(before || ' → ' || after, ',') from sync_events where kind = 'title_changed'`); got != "The Way of Kings (Unabridged) → The Way of Kings" {
		t.Errorf("title events %q", got)
	}

	// A user-created manual item is the user's, even when sync finds it by ISBN.
	c := run(t, e, acct)
	if n := events(t, e, acct)[sync.EventTitleChanged]; n != 1 || c.Err != nil {
		t.Errorf("re-run recorded %d title events", n)
	}
	exec(t, e, `update media_items set created_by_user_id = $2, title = 'Mine (Unabridged)' where id = $1`, wk, alice.ID)
	run(t, e, acct)
	if got := scalar[string](t, e.s, `select title from media_items where id = $1`, wk); got != "Mine (Unabridged)" {
		t.Errorf("user-created item title changed to %q", got)
	}
}

func pandoraBody(title string) string {
	return bookBody(itemPandora, title, "Peter F. Hamilton", "John Lee", "", `"2004"`, "Commonwealth Saga #1", 133200)
}

func TestManualItemMatchedLaterOntoSameItem(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.addBooks(1, pandoraBody("Pandora's Star (Unabridged)"))
	run(t, e, acct)
	ps := itemFor(t, e, itemPandora)
	if got := scalar[string](t, e.s, `select title || '|' || (metadata_provider is null) from media_items where id = $1`, ps); got != "Pandora's Star|true" {
		t.Fatalf("first import %q", got)
	}
	// Simulate the pre-cleanup import and some user data on the entry.
	exec(t, e, `update media_items set title = 'Pandora''s Star (Unabridged)' where id = $1`, ps)
	exec(t, e, `update library_entries set notes = 'mine', played_user = true where user_id = $1 and media_item_id = $2`, alice.ID, ps)
	entry := scalar[int64](t, e.s, `select id from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, ps)
	items := scalar[int](t, e.s, `select count(*) from media_items`)

	e.ol.set(true)
	res := run(t, e, acct)
	if res.Err != nil || res.Counts.ItemsResolved != 1 || res.Counts.ItemsCreated != 0 || res.Counts.EntriesCreated != 0 {
		t.Fatalf("rematch %+v %v %v", res.Counts, res.Err, res.Warnings)
	}
	if got := scalar[string](t, e.s, `select title || '|' || metadata_provider from media_items where id = $1`, ps); got != "Pandora's Star|openlibrary" {
		t.Errorf("rematched item %q", got)
	}
	if got := scalar[int64](t, e.s, `select media_item_id from external_ids where provider = 'openlibrary_work' and value = 'OL2W'`); got != ps {
		t.Errorf("OL2W on item %d, want %d", got, ps)
	}
	if n := scalar[int](t, e.s, `select count(*) from media_items`); n != items {
		t.Errorf("rematch created items: %d -> %d", items, n)
	}
	if got := scalar[string](t, e.s, `select id || ' ' || notes || ' ' || played_user from library_entries where user_id = $1 and media_item_id = $2`, alice.ID, ps); got != itoa(entry)+" mine true" {
		t.Errorf("entry after rematch %q", got)
	}
	if got := scalar[string](t, e.s, `select before || ' → ' || after from sync_events where kind = 'item_matched' and media_item_id = $1`, ps); got != "Pandora's Star (Unabridged) → Pandora's Star" {
		t.Errorf("match event %q", got)
	}
	if got := text(e.s.LoginAs(t, alice).Get("/settings/connections/" + acct.PublicID.String() + "/history?filter=needs_review").Body); strings.Contains(got, "Pandora") {
		t.Error("matched item still needs review")
	}

	// Idempotent afterwards.
	lookups := e.ol.lookups()
	res = run(t, e, acct)
	if res.Counts.ItemsResolved != 0 || e.ol.lookups() != lookups || events(t, e, acct)[sync.EventItemMatched] != 1 {
		t.Errorf("re-run %+v lookups %d -> %d", res.Counts, lookups, e.ol.lookups())
	}
}

// When the provider work is already another catalog item, the manual item only gets its title fixed.
func TestManualItemNotMergedIntoExistingItem(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.addBooks(1, pandoraBody("Pandora's Star (Unabridged)"))
	run(t, e, acct)
	ps := itemFor(t, e, itemPandora)
	exec(t, e, `update media_items set title = 'Pandora''s Star (Unabridged)' where id = $1`, ps)

	e.ol.set(true)
	it, err := openlibrary.NewWithHTTP(e.ol.URL, e.ad.HTTP).Fetch(context.Background(), db.MediaKindBook, "OL2W")
	if err != nil {
		t.Fatal(err)
	}
	other, err := discover.Import(context.Background(), e.s.Deps, "openlibrary", it, &alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, e, acct)
	if res.Err != nil || res.Counts.ItemsResolved != 0 {
		t.Fatalf("run %+v %v", res.Counts, res.Err)
	}
	if got := scalar[string](t, e.s, `select title || '|' || (metadata_provider is null) from media_items where id = $1`, ps); got != "Pandora's Star|true" || other.ID == ps {
		t.Errorf("manual item %q", got)
	}
	if itemFor(t, e, itemPandora) != ps {
		t.Error("the ABS item moved")
	}
}

func TestLookupBudgetDefersMatching(t *testing.T) {
	e := newEnv(t)
	e.ad.LookupBudget = 3
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.addBooks(1, fillerBooks(1, 4)...)

	before := time.Now()
	res := run(t, e, acct)
	if c := res.Counts; res.Err != nil || c.Games != 7 || c.Deferred != 4 || c.ItemsCreated+c.ItemsResolved != 3 || c.EntriesCreated != 3 || c.Skipped != 0 {
		t.Fatalf("first run %+v %v", res.Counts, res.Err)
	}
	queued := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, "queued") {
			queued++
			if w != "4 books queued for matching next sync" {
				t.Errorf("warning %q", w)
			}
		}
	}
	if queued != 1 {
		t.Errorf("warnings %v", res.Warnings)
	}
	if n := scalar[int](t, e.s, `select count(*) from media_items`); n != 3 {
		t.Errorf("deferred books created items: %d", n)
	}
	next := scalar[time.Time](t, e.s, `select next_sync_at from connected_accounts where id = $1`, acct.ID)
	if next.Before(before.Add(13*time.Minute)) || next.After(time.Now().Add(17*time.Minute)) {
		t.Errorf("next sync %s, want ~15 min", next.Sub(before))
	}

	res = run(t, e, acct)
	if res.Counts.Deferred != 1 || res.Counts.EntriesCreated != 3 {
		t.Fatalf("second run %+v", res.Counts)
	}
	res = run(t, e, acct)
	if res.Counts.Deferred != 0 || res.Counts.EntriesCreated != 1 {
		t.Fatalf("third run %+v", res.Counts)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "queued") {
			t.Errorf("warning %q after everything matched", w)
		}
	}
	if next := scalar[time.Time](t, e.s, `select next_sync_at from connected_accounts where id = $1`, acct.ID); next.Before(time.Now().Add(10 * time.Hour)) {
		t.Errorf("next sync %s, want the default interval", time.Until(next))
	}
	if n := scalar[int](t, e.s, `select count(*) from library_entries where user_id = $1`, alice.ID); n != 7 {
		t.Errorf("%d entries", n)
	}

	// The summary reaches the connections card.
	body := text(e.s.LoginAs(t, alice).Get("/settings/connections").Body)
	if !strings.Contains(body, "7 books in your library, 2 with progress") {
		t.Errorf("connections card: %s", body)
	}
}
