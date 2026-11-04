package lists_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestVisibilityChangeUpdatesActivity(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	item := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	l := testutil.CreateList(t, s.Pool, u.ID, "Faves", db.VisibilityPublic, item.ID)
	if _, err := activity.Record(context.Background(), s.Q, activity.Event{UserID: u.ID, Verb: db.ActivityVerbListed, MediaItemID: item.ID, ListID: &l.ID, Visibility: db.VisibilityPublic}); err != nil {
		t.Fatal(err)
	}
	c := s.LoginAs(t, u)
	c.PostForm("/lists/"+l.PublicID.String()+"/edit", url.Values{"name": {"Faves"}, "visibility": {"private"}})

	var vis db.Visibility
	if err := s.Pool.QueryRow(context.Background(), "select visibility from activity_events where list_id = $1", l.ID).Scan(&vis); err != nil {
		t.Fatal(err)
	}
	if vis != db.VisibilityPrivate {
		t.Fatalf("activity visibility = %s, want private", vis)
	}
}
