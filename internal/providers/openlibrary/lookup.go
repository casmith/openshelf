package openlibrary

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/casmith/openshelf/internal/providers"
)

// FindByISBN returns the work ID (OL…W) for an ISBN-10 or ISBN-13, or
// providers.ErrNotFound when Open Library doesn't know it.
func (p *Provider) FindByISBN(ctx context.Context, isbn string) (string, error) {
	isbn = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(isbn), "-", ""), " ", "")
	if isbn == "" {
		return "", providers.ErrNotFound
	}
	var ed struct {
		Works []struct {
			Key string `json:"key"`
		} `json:"works"`
	}
	if err := p.http.GetJSON(ctx, p.BaseURL+"/isbn/"+url.PathEscape(isbn)+".json", nil, &ed); err != nil {
		return "", err
	}
	for _, w := range ed.Works {
		if id := strings.TrimPrefix(w.Key, "/works/"); workIDRe.MatchString(id) {
			return id, nil
		}
	}
	return "", providers.ErrNotFound
}

// FindWork returns the work whose title and one of whose authors match exactly
// (ignoring case and punctuation), or providers.ErrNotFound.
func (p *Provider) FindWork(ctx context.Context, title, author string) (string, error) {
	nt, na := normalize(title), normalize(author)
	if nt == "" || na == "" {
		return "", providers.ErrNotFound
	}
	v := url.Values{
		"title":  {title},
		"author": {author},
		"fields": {"key,title,author_name"},
		"limit":  {"10"},
	}
	var res struct {
		Docs []searchDoc `json:"docs"`
	}
	if err := p.http.GetJSON(ctx, p.BaseURL+"/search.json?"+v.Encode(), nil, &res); err != nil {
		return "", err
	}
	for _, d := range res.Docs {
		id := strings.TrimPrefix(d.Key, "/works/")
		if !workIDRe.MatchString(id) || normalize(d.Title) != nt {
			continue
		}
		for _, a := range d.AuthorName {
			if normalize(a) == na {
				return id, nil
			}
		}
	}
	return "", providers.ErrNotFound
}

func normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}
