package audiobookshelf_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/sync/synctest"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestSyncEmitsBooks(t *testing.T) {
	e := newEnv(t)
	alice := testutil.CreateUser(t, e.s.Q, "alice")
	acct := e.linkAccount(t, alice, aliceABSID, aliceKey)
	e.abs.Items[itemKings] = bookBody(itemKings, "The Way of Kings", `[{"id":"au2","name":"Brandon Sanderson"}]`, `[]`, "0-306-40615-2", `2010`, `[]`, 165600)

	rec := &synctest.Recorder{}
	cursor, err := e.ad.Sync(context.Background(), acct, nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Games) != 2 || rec.Games[0].Key != itemHailMary || rec.Games[1].Key != itemKings {
		t.Fatalf("emitted %+v", rec.Games)
	}
	hm := rec.Games[0]
	if hm.Kind != db.MediaKindBook || hm.Title != "Project Hail Mary" || hm.ExternalIDs[0].Provider != "audiobookshelf_item" ||
		hm.Edition.ExternalIDs[0].Value != "9780593135204" || *hm.Edition.DurationMinutes != 720 || hm.Progress.Finished {
		t.Errorf("hail mary %+v", hm)
	}
	var wk []string
	for _, x := range rec.Games[1].Edition.ExternalIDs {
		wk = append(wk, x.Provider+":"+x.Value)
	}
	if len(wk) != 2 || wk[0] != "isbn13:9780306406157" || wk[1] != "isbn10:0306406152" {
		t.Errorf("isbn-10 ids %v", wk)
	}
	if rec.Games[1].ReleaseYear != 2010 || !rec.Games[1].Progress.Finished || rec.Games[1].Progress.FinishedAt == nil {
		t.Errorf("kings %+v", rec.Games[1])
	}
	if string(cursor) != `{"last_update":`+itoa(ms(lastListened))+`}` {
		t.Errorf("cursor %s", cursor)
	}

	rec.Err = context.Canceled
	if _, err := e.ad.Sync(context.Background(), acct, nil, rec); !errors.Is(err, context.Canceled) {
		t.Errorf("emit error not propagated: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
