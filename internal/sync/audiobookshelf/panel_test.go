package audiobookshelf_test

import (
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/testutil"
)

func TestItemPanelShowsProgress(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	run(t, e, acct)
	c := e.s.LoginAs(t, alice)

	itemURL := func(key string) string {
		return "/items/" + scalar[string](t, e.s, `select public_id::text from media_items where id = $1`, itemFor(t, e, key))
	}
	page := text(c.Get(itemURL(itemHailMary)).Body)
	for _, want := range []string{"Synced sources", "Audiobookshelf · 43% · 5 h 12 m of 12 h · last listened Mar 3, 2026", "Exclude from sync"} {
		if !strings.Contains(page, want) {
			t.Errorf("hail mary page lacks %q", want)
		}
	}
	page = text(c.Get(itemURL(itemKings)).Body)
	if !strings.Contains(page, "Finished on Jan 15, 2026 per Audiobookshelf") {
		t.Error("kings page lacks the finish date")
	}

	// Excluding keeps the data and stops the import; including resumes it.
	accountID := scalar[string](t, e.s, `select public_id::text from connected_accounts where id = $1`, acct.ID)
	res := c.PostForm("/connections/"+accountID+"/exclusions", map[string][]string{"key": {itemHailMary}, "next": {itemURL(itemHailMary)}})
	if res.StatusCode != 303 {
		t.Fatalf("exclude: %d", res.StatusCode)
	}
	if page := text(c.Get(itemURL(itemHailMary)).Body); !strings.Contains(page, "Excluded from sync") || !strings.Contains(page, "Include again") {
		t.Error("excluded state not shown")
	}
	if got := scalar[int64](t, e.s, `select media_item_id from import_exclusions where account_id = $1 and provider_item_key = $2`, acct.ID, itemHailMary); got != itemFor(t, e, itemHailMary) {
		t.Errorf("exclusion item %d", got)
	}
	e.abs.setUser(aliceKey, meBody(aliceABSID, "alice_abs", aliceProgress(0.9, 38880, false)))
	run(t, e, acct)
	if page := text(c.Get(itemURL(itemHailMary)).Body); !strings.Contains(page, "43%") {
		t.Error("excluded item's progress changed")
	}

	first := scalar[string](t, e.s, `select public_id::text from sync_runs where account_id = $1 order by id limit 1`, acct.ID)
	hist := text(c.Get("/settings/connections/" + accountID + "/history?run=" + first).Body)
	if !strings.Contains(hist, "Finished on Jan 15, 2026 per Audiobookshelf") || !strings.Contains(hist, "Progress: none → 43%") {
		t.Error("history doesn't render progress events")
	}
}
