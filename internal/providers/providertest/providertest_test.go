package providertest

import (
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

func TestFakeConforms(t *testing.T) {
	f := &Fake{Kind: db.MediaKindBook, Items: map[string]*providers.Item{
		"OL1W": {
			Kind: db.MediaKindBook, Title: "Pandora's Star", ReleaseYear: 2004, Genres: []string{"science fiction"},
			Credits:     []providers.Credit{{Name: "Peter F. Hamilton", Role: db.CreditRoleAuthor}},
			ExternalIDs: []providers.ExternalID{{Provider: "openlibrary_work", Value: "OL1W"}},
			Editions:    []providers.Edition{{Format: db.EditionFormatPaperback}},
		},
	}}
	Conformance(t, f, []Case{{Kind: db.MediaKindBook, Query: "pandora", WantTitle: "Pandora's Star"}})
}

func TestRegistryFor(t *testing.T) {
	r := providers.NewRegistry(
		&Fake{ProviderName: "a", Kind: db.MediaKindBook},
		&Fake{ProviderName: "b", Kind: db.MediaKindBook, Unconfigured: true},
		&Fake{ProviderName: "c", Kind: db.MediaKindFilm},
	)
	got := r.For(db.MediaKindBook)
	if len(got) != 1 || got[0].Name() != "a" {
		t.Fatalf("For(book) = %v", got)
	}
}
