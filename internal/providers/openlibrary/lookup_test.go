package openlibrary_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/providers/openlibrary"
)

func TestFindByISBNAndWork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/isbn/9780593135204.json":
			fmt.Fprint(w, `{"key":"/books/OL1M","works":[{"key":"/works/OL1W"}]}`)
		case "/search.json":
			if r.URL.Query().Get("title") != "Dune" || r.URL.Query().Get("author") != "Frank Herbert" {
				t.Errorf("search query %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"docs":[{"key":"/works/OL2W","title":"Dune Messiah","author_name":["Frank Herbert"]},
				{"key":"/works/OL3W","title":"Dune","author_name":["Someone Else"]},
				{"key":"/works/OL4W","title":"DUNE!","author_name":["Kevin J. Anderson","frank  herbert"]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := openlibrary.NewWithURL(srv.URL)
	ctx := context.Background()

	if id, err := p.FindByISBN(ctx, "978-0-593-13520-4"); err != nil || id != "OL1W" {
		t.Errorf("FindByISBN = %q, %v", id, err)
	}
	if _, err := p.FindByISBN(ctx, "9780000000002"); !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("unknown isbn: %v", err)
	}
	if id, err := p.FindWork(ctx, "Dune", "Frank Herbert"); err != nil || id != "OL4W" {
		t.Errorf("FindWork = %q, %v", id, err)
	}
	if _, err := p.FindWork(ctx, "Dune", ""); !errors.Is(err, providers.ErrNotFound) {
		t.Errorf("no author: %v", err)
	}
}
