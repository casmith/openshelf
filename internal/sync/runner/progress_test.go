package runner_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
	"github.com/casmith/openshelf/internal/testutil"
)

func i32(v int32) *int32 { return &v }

func book(key string, p *sync.Progress) sync.OwnedItem {
	return sync.OwnedItem{
		Kind: db.MediaKindBook, Key: key, Title: "Book " + key,
		ExternalIDs: []providers.ExternalID{{Provider: "fake_book", Value: key}},
		Credits:     []providers.Credit{{Name: "An Author", Role: db.CreditRoleAuthor}, {Name: "A Narrator", Role: db.CreditRoleNarrator}},
		Edition: &providers.Edition{
			Format: db.EditionFormatAudiobook, DurationMinutes: i32(600),
			ExternalIDs: []providers.ExternalID{{Provider: "isbn13", Value: "97800000000" + key}},
		},
		Progress: p,
	}
}

// Books with progress go through the same guard as games: only source columns and
// source tables change, and an existing edition's ISBN matches the item.
func TestProgressImportUnderGuard(t *testing.T) {
	e := setup(t)
	existing := testutil.CreateItem(t, e.s.Pool, db.MediaKindBook, "Existing Book")
	ed := testutil.CreateEdition(t, e.s.Pool, existing.ID, "")
	if _, err := e.s.Pool.Exec(t.Context(), `insert into external_ids (provider, value, edition_id) values ('isbn13', '9780000000002', $1)`, ed.ID); err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	e.fake.Games = []sync.OwnedItem{
		book("01", &sync.Progress{Fraction: 0.25, CurrentSeconds: i32(9000), DurationSeconds: i32(36000), LastActivityAt: &last}),
		book("02", &sync.Progress{Fraction: 0.5}),
		book("03", &sync.Progress{}),
	}
	res := e.run(t)
	if res.Err != nil || res.Counts.ItemsCreated != 2 || res.Counts.EntriesCreated != 3 {
		t.Fatalf("counts %+v err %v", res.Counts, res.Err)
	}
	if got := scalar[int64](t, e.s.Pool, `select media_item_id from external_ids where provider = 'fake_book' and value = '02'`); got != existing.ID {
		t.Errorf("isbn didn't match the existing edition's item: %d", got)
	}
	b1 := scalar[int64](t, e.s.Pool, `select media_item_id from external_ids where provider = 'fake_book' and value = '01'`)
	if got := scalar[string](t, e.s.Pool, `select m.kind::text || ' ' || (select count(*) from credits where media_item_id = m.id) || ' ' ||
		(select count(*) from book_details where media_item_id = m.id) from media_items m where id = $1`, b1); got != "book 2 1" {
		t.Errorf("manual book %q", got)
	}
	if got := scalar[int64](t, e.s.Pool, `select e.media_item_id from external_ids x join editions e on e.id = x.edition_id where x.provider = 'isbn13' and x.value = '9780000000001'`); got != b1 {
		t.Errorf("isbn attached to %d", got)
	}
	if r := entry(t, e, e.user.ID, b1); r.PlayedSource == nil || !*r.PlayedSource {
		t.Errorf("progress didn't set played_source: %+v", r)
	}
	b3 := scalar[int64](t, e.s.Pool, `select media_item_id from external_ids where provider = 'fake_book' and value = '03'`)
	if r := entry(t, e, e.user.ID, b3); r.PlayedSource != nil {
		t.Errorf("zero progress set played_source: %+v", r)
	}
	if n := countEvents(t, e, sync.EventProgressChanged); n != 2 {
		t.Errorf("%d progress events", n)
	}

	// A user's "not played" survives more progress, and nothing is written twice.
	userAction(t, e.s.Pool, `update library_entries set played_user = false where media_item_id = $1`, b1)
	e.fake.Games[0].Progress.Fraction = 1
	e.fake.Games[0].Progress.Finished = true
	e.run(t)
	e.run(t)
	if r := entry(t, e, e.user.ID, b1); r.PlayedUser == nil || *r.PlayedUser {
		t.Errorf("played_user changed: %+v", r)
	}
	if countEvents(t, e, sync.EventProgressChanged) != 3 || countEvents(t, e, sync.EventSourceFinished) != 1 || countEvents(t, e, sync.EventOverrideRespected) != 1 {
		t.Errorf("events: progress %d finished %d respected %d", countEvents(t, e, sync.EventProgressChanged),
			countEvents(t, e, sync.EventSourceFinished), countEvents(t, e, sync.EventOverrideRespected))
	}
	if got := scalar[string](t, e.s.Pool, `select fraction || ' ' || finished || ' ' || current_seconds || ' ' || (last_activity_at is not null) from external_progress where provider_item_key = '01'`); got != "1 true 9000 true" {
		t.Errorf("progress row %q", got)
	}
}

// Source-owned tables (ADR 0002) are written only by the sync runner's queries.
func TestSourceTablesWrittenOnlyBySync(t *testing.T) {
	files, err := filepath.Glob("../../db/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	writes := regexp.MustCompile(`(?i)\b(insert\s+into|update|delete\s+from)\s+(external_progress|external_playtime|achievement_unlocks)\b`)
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "sync") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := writes.FindString(stripComments(string(b))); m != "" {
			t.Errorf("%s writes a source table: %s", f, m)
		}
	}
}
