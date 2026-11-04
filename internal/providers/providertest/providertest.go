// Package providertest holds the shared contract test every metadata adapter
// runs against recorded fixtures, plus an in-memory Fake provider.
package providertest

import (
	"context"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

type Case struct {
	Kind      db.MediaKind
	Query     string
	WantTitle string // a result with this title must appear and be fetchable
}

func Conformance(t *testing.T, p providers.Provider, cases []Case) {
	t.Helper()
	ctx := context.Background()
	if p.Name() == "" || len(p.Kinds()) == 0 {
		t.Fatal("provider must have a name and at least one kind")
	}
	if !p.Configured() {
		t.Fatal("provider under test must be configured")
	}
	for _, c := range cases {
		t.Run(string(c.Kind)+"/"+c.Query, func(t *testing.T) {
			results, err := p.Search(ctx, c.Kind, c.Query)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			var hit *providers.SearchResult
			for i, r := range results {
				if r.Provider != p.Name() {
					t.Errorf("result %d: Provider = %q, want %q", i, r.Provider, p.Name())
				}
				if r.Kind != c.Kind || r.Title == "" || r.ExternalID.Provider == "" || r.ExternalID.Value == "" {
					t.Errorf("result %d incomplete: %+v", i, r)
				}
				if hit == nil && r.Title == c.WantTitle {
					hit = &results[i]
				}
			}
			if hit == nil {
				t.Fatalf("no result titled %q in %d results", c.WantTitle, len(results))
			}

			item, err := p.Fetch(ctx, c.Kind, hit.ExternalID.Value)
			if err != nil {
				t.Fatalf("Fetch(%q): %v", hit.ExternalID.Value, err)
			}
			CheckItem(t, item, c.Kind)
			found := false
			for _, x := range item.ExternalIDs {
				if x == hit.ExternalID || (x.Provider == hit.ExternalID.Provider && x.Value == hit.ExternalID.Value) {
					found = true
				}
			}
			if !found {
				t.Errorf("fetched item's ExternalIDs %v don't include search ID %v", item.ExternalIDs, hit.ExternalID)
			}
		})
	}
}

// CheckItem validates a normalized item against the data-model constraints.
func CheckItem(t *testing.T, item *providers.Item, kind db.MediaKind) {
	t.Helper()
	if item == nil {
		t.Fatal("nil item")
	}
	if item.Kind != kind {
		t.Errorf("Kind = %q, want %q", item.Kind, kind)
	}
	if item.Title == "" {
		t.Error("empty Title")
	}
	if len(item.ExternalIDs) == 0 {
		t.Error("no ExternalIDs")
	}
	for _, x := range item.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			t.Errorf("incomplete external ID %+v", x)
		}
	}
	for _, g := range item.Genres {
		if g != strings.ToLower(g) || strings.TrimSpace(g) == "" {
			t.Errorf("genre %q must be lowercase and non-empty", g)
		}
	}
	for _, c := range item.Credits {
		if !c.Role.Valid() || c.Name == "" {
			t.Errorf("invalid credit %+v", c)
		}
	}
	for _, e := range item.Editions {
		if e.Format != "" && !e.Format.Valid() {
			t.Errorf("invalid edition format %q", e.Format)
		}
		if e.Platform != nil && kind != db.MediaKindGame {
			t.Errorf("non-game edition has a platform")
		}
	}
	details := 0
	for _, ok := range []bool{item.Game != nil, item.Book != nil, item.Film != nil, item.Show != nil} {
		if ok {
			details++
		}
	}
	if details > 1 {
		t.Error("item has details for more than one kind")
	}
}

// Fake is an in-memory provider for tests.
type Fake struct {
	ProviderName string
	Kind         db.MediaKind
	Items        map[string]*providers.Item // keyed by external ID value
	Unconfigured bool
	Err          error
}

func (f *Fake) Name() string {
	if f.ProviderName == "" {
		return "fake"
	}
	return f.ProviderName
}
func (f *Fake) Kinds() []db.MediaKind { return []db.MediaKind{f.Kind} }
func (f *Fake) Configured() bool      { return !f.Unconfigured }

func (f *Fake) Search(ctx context.Context, kind db.MediaKind, query string) ([]providers.SearchResult, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	var out []providers.SearchResult
	for id, it := range f.Items {
		if it.Kind == kind && strings.Contains(strings.ToLower(it.Title), strings.ToLower(query)) {
			out = append(out, providers.SearchResult{
				Provider: f.Name(), ExternalID: providers.ExternalID{Provider: it.ExternalIDs[0].Provider, Value: id},
				Kind: kind, Title: it.Title, Year: it.ReleaseYear, CoverURL: it.CoverURL,
			})
		}
	}
	return out, nil
}

func (f *Fake) Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*providers.Item, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	it, ok := f.Items[externalID]
	if !ok || it.Kind != kind {
		return nil, providers.ErrNotFound
	}
	return it, nil
}
