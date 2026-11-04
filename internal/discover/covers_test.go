package discover_test

import (
	"context"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/providertest"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestBackfillCoversExactTitleOnly(t *testing.T) {
	s := testutil.NewServer(t)
	ctx := context.Background()
	dune := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dune")
	other := testutil.CreateItem(t, s.Pool, db.MediaKindBook, "Dun")
	if _, err := s.Pool.Exec(ctx, "update media_items set release_year = 1965 where id = $1", dune.ID); err != nil {
		t.Fatal(err)
	}
	s.Deps.Providers = providers.NewRegistry(&providertest.Fake{Kind: db.MediaKindBook, Items: map[string]*providers.Item{
		"OL1W": {Kind: db.MediaKindBook, Title: "Dune", ReleaseYear: 1965, CoverURL: "https://covers.example/dune.jpg",
			ExternalIDs: []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL1W"}}},
		"OL2W": {Kind: db.MediaKindBook, Title: "Dune Messiah", ReleaseYear: 1969, CoverURL: "https://covers.example/messiah.jpg",
			ExternalIDs: []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL2W"}}},
	}})

	n, err := discover.BackfillCovers(ctx, s.Deps)
	if err != nil || n != 1 {
		t.Fatalf("BackfillCovers = %d, %v; want 1", n, err)
	}
	got, _ := s.Q.GetMediaItemByID(ctx, dune.ID)
	if got.CoverImageID == nil {
		t.Fatal("Dune should have a cover")
	}
	img, _ := s.Q.GetImage(ctx, *got.CoverImageID)
	if img.SourceUrl == nil || *img.SourceUrl != "https://covers.example/dune.jpg" {
		t.Fatalf("cover source = %v", img.SourceUrl)
	}
	if o, _ := s.Q.GetMediaItemByID(ctx, other.ID); o.CoverImageID != nil {
		t.Fatal("non-matching title must not get a cover")
	}
	if n, _ := discover.BackfillCovers(ctx, s.Deps); n != 0 {
		t.Fatalf("second run found %d, want 0", n)
	}
}
