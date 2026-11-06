package audiobookshelf_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync/audiobookshelf"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestSyncEmitsBooks(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.Libraries[1].Items[1] = bookBody(itemKings, "The Way of Kings (Unabridged)", "Brandon Sanderson", "", "0-306-40615-2", `2010`, "The Stormlight Archive #1, Cosmere #3", 165600)

	rec := &synctest.Recorder{}
	cursor, err := e.ad.Sync(context.Background(), acct, nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Games) != 3 || rec.Games[0].Key != itemHailMary || rec.Games[1].Key != itemKings || rec.Games[2].Key != itemUnread {
		t.Fatalf("emitted %+v", rec.Games)
	}
	hm := rec.Games[0]
	if hm.Kind != db.MediaKindBook || hm.Title != "Project Hail Mary" || hm.ExternalIDs[0].Provider != "audiobookshelf_item" ||
		hm.Edition.ExternalIDs[0].Value != "9780593135204" || *hm.Edition.DurationMinutes != 720 || hm.Progress.Finished {
		t.Errorf("hail mary %+v", hm)
	}
	if len(hm.Credits) != 2 || hm.Credits[0].Name != "Andy Weir" || hm.Credits[1].Name != "Ray Porter" || hm.Credits[1].Role != db.CreditRoleNarrator {
		t.Errorf("hail mary credits %+v", hm.Credits)
	}
	wk := rec.Games[1]
	var ids []string
	for _, x := range wk.Edition.ExternalIDs {
		ids = append(ids, x.Provider+":"+x.Value)
	}
	if len(ids) != 2 || ids[0] != "isbn13:9780306406157" || ids[1] != "isbn10:0306406152" {
		t.Errorf("isbn-10 ids %v", ids)
	}
	if wk.Title != "The Way of Kings" || wk.ReleaseYear != 2010 || !wk.Progress.Finished || wk.Progress.FinishedAt == nil ||
		wk.Book.SeriesName != "The Stormlight Archive" || *wk.Book.SeriesPosition != 1 {
		t.Errorf("kings %+v %+v", wk, wk.Book)
	}
	if un := rec.Games[2]; un.Progress != nil || len(un.Credits) != 0 || *un.Edition.DurationMinutes != 60 {
		t.Errorf("untouched book %+v", un)
	}
	if string(cursor) != `{"last_update":`+itoa(ms(lastListened))+`}` {
		t.Errorf("cursor %s", cursor)
	}
	if e.abs.hits("/api/libraries/lib_pod/items") != 0 {
		t.Error("listed the podcast library")
	}
	if q := e.abs.Queries; len(q) != 1 || !strings.Contains(q[0], "limit=100") || !strings.Contains(q[0], "minified=1") || !strings.Contains(q[0], "page=0") {
		t.Errorf("item list queries %v", q)
	}
	if len(rec.Warnings) != 1 || !strings.Contains(rec.Warnings[0], "Skipped 1 listening entry") {
		t.Errorf("warnings %v", rec.Warnings)
	}

	rec.Err = context.Canceled
	if _, err := e.ad.Sync(context.Background(), acct, nil, rec); !errors.Is(err, context.Canceled) {
		t.Errorf("emit error not propagated: %v", err)
	}
}

func TestSyncPagesThroughLibraries(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.addBooks(1, fillerBooks(1, 230)...)
	e.abs.Libraries = append(e.abs.Libraries, &FakeLibrary{ID: "lib_more", MediaType: "book", Items: append(fillerBooks(231, 5), bookBody(itemHailMary, "Duplicate", "", "", "", "null", "", 1))})

	rec := &synctest.Recorder{}
	if _, err := e.ad.Sync(context.Background(), acct, nil, rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Games) != 3+230+5 {
		t.Fatalf("emitted %d books", len(rec.Games))
	}
	if n := e.abs.hits("/api/libraries/lib_books/items"); n != 3 {
		t.Errorf("%d pages of 233 items", n)
	}
	withProgress := 0
	for _, g := range rec.Games {
		if g.Kind != db.MediaKindBook {
			t.Errorf("emitted %s", g.Kind)
		}
		if g.Progress != nil {
			withProgress++
		}
	}
	if withProgress != 2 {
		t.Errorf("%d books with progress", withProgress)
	}
}

func fillerBooks(from, n int) []string {
	out := make([]string, n)
	for i := range out {
		id := fmt.Sprintf("li_filler_%03d", from+i)
		out[i] = bookBody(id, fmt.Sprintf("Filler %d (Unabridged)", from+i), "Some Author", "", "", "null", "", 600)
	}
	return out
}

func TestCleanTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Pandora's Star (Unabridged)", "Pandora's Star"},
		{"The Fellowship of the Ring (Unabridged)", "The Fellowship of the Ring"},
		{"Dune (Abridged)", "Dune"},
		{"Dune [Unabridged]", "Dune"},
		{"Dune: Unabridged", "Dune"},
		{"Dune - Unabridged", "Dune"},
		{"Dune – unabridged edition", "Dune"},
		{"Dune (Unabridged Edition)", "Dune"},
		{"The Sandman (Dramatized Adaptation)", "The Sandman"},
		{"The Martian: A Novel", "The Martian"},
		{"Educated (A Novel)", "Educated"},
		{"Circe: A Novel (Unabridged)", "Circe"},
		{"  Little Brother  ", "Little Brother"},
		{"Managing Humans", "Managing Humans"},
		{"A Novel", "A Novel"},
		{"(Unabridged)", "(Unabridged)"},
		{"Not a Novel", "Not a Novel"},
		{"Spider-Man: Unabridged Tales", "Spider-Man: Unabridged Tales"},
		{"Abridged", "Abridged"},
	} {
		if got := audiobookshelf.CleanTitle(tc.in); got != tc.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
