// Package openlibrary is the Open Library metadata adapter for books. It needs no
// key; requests carry the contact user agent their API policy asks for.
package openlibrary

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

const (
	DefaultBaseURL = "https://openlibrary.org"
	coversBaseURL  = "https://covers.openlibrary.org"

	maxEditions = 20
	maxAuthors  = 5
	maxGenres   = 5
)

var (
	workIDRe   = regexp.MustCompile(`^OL\d+W$`)
	authorKey  = regexp.MustCompile(`^/authors/OL\d+A$`)
	editionKey = regexp.MustCompile(`^/books/(OL\d+M)$`)
)

type Provider struct {
	BaseURL string
	http    *providers.HTTP
}

func New(cfg config.Config) *Provider { return NewWithURL(DefaultBaseURL) }

// NewWithURL points the adapter at another host; tests use an httptest server.
func NewWithURL(baseURL string) *Provider {
	return NewWithHTTP(baseURL, providers.NewHTTP(1, 3))
}

// NewWithHTTP is NewWithURL with a caller-supplied client (tests lift the rate limit).
func NewWithHTTP(baseURL string, h *providers.HTTP) *Provider {
	return &Provider{BaseURL: strings.TrimRight(baseURL, "/"), http: h}
}

func (p *Provider) Name() string          { return "openlibrary" }
func (p *Provider) Kinds() []db.MediaKind { return []db.MediaKind{db.MediaKindBook} }
func (p *Provider) Configured() bool      { return true }

type searchDoc struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	CoverI           int      `json:"cover_i"`
}

func (p *Provider) Search(ctx context.Context, kind db.MediaKind, query string) ([]providers.SearchResult, error) {
	if kind != db.MediaKindBook || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	v := url.Values{
		"q":      {query},
		"fields": {"key,title,author_name,first_publish_year,cover_i,isbn"},
		"limit":  {"20"},
	}
	var res struct {
		Docs []searchDoc `json:"docs"`
	}
	if err := p.http.GetJSON(ctx, p.BaseURL+"/search.json?"+v.Encode(), nil, &res); err != nil {
		return nil, err
	}
	out := make([]providers.SearchResult, 0, len(res.Docs))
	for _, d := range res.Docs {
		id := strings.TrimPrefix(d.Key, "/works/")
		if !workIDRe.MatchString(id) || d.Title == "" {
			continue
		}
		out = append(out, providers.SearchResult{
			Provider:   p.Name(),
			ExternalID: workExternalID(id),
			Kind:       db.MediaKindBook,
			Title:      d.Title,
			Year:       d.FirstPublishYear,
			Subtitle:   strings.Join(firstN(d.AuthorName, 3), ", "),
			CoverURL:   coverURL(d.CoverI),
		})
	}
	return out, nil
}

type work struct {
	Key              string          `json:"key"`
	Title            string          `json:"title"`
	Subtitle         string          `json:"subtitle"`
	Description      json.RawMessage `json:"description"`
	Subjects         []string        `json:"subjects"`
	Covers           []int           `json:"covers"`
	FirstPublishDate string          `json:"first_publish_date"`
	Authors          []struct {
		Author struct {
			Key string `json:"key"`
		} `json:"author"`
	} `json:"authors"`
}

type edition struct {
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Publishers    []string `json:"publishers"`
	PublishDate   string   `json:"publish_date"`
	NumberOfPages int32    `json:"number_of_pages"`
	PhysicalFmt   string   `json:"physical_format"`
	ISBN13        []string `json:"isbn_13"`
	ISBN10        []string `json:"isbn_10"`
}

func (p *Provider) Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*providers.Item, error) {
	if kind != db.MediaKindBook || !workIDRe.MatchString(externalID) {
		return nil, providers.ErrNotFound
	}
	var w work
	if err := p.http.GetJSON(ctx, p.BaseURL+"/works/"+externalID+".json", nil, &w); err != nil {
		return nil, err
	}
	if w.Title == "" {
		return nil, providers.ErrNotFound
	}
	item := &providers.Item{
		Kind:        db.MediaKindBook,
		Title:       w.Title,
		Synopsis:    description(w.Description),
		Genres:      normalizeGenres(w.Subjects),
		ExternalIDs: []providers.ExternalID{workExternalID(externalID)},
		Attribution: "Metadata from Open Library",
		Book:        &providers.BookDetails{},
	}
	for _, c := range w.Covers {
		if c > 0 {
			item.CoverURL = coverURL(c)
			break
		}
	}
	if t, full, ok := parseDate(w.FirstPublishDate); ok {
		item.ReleaseYear = t.Year()
		if full {
			item.ReleaseDate = &t
		}
	}

	for i, a := range w.Authors {
		if i >= maxAuthors || !authorKey.MatchString(a.Author.Key) {
			continue
		}
		var au struct {
			Name string `json:"name"`
		}
		if err := p.http.GetJSON(ctx, p.BaseURL+a.Author.Key+".json", nil, &au); err != nil {
			return nil, err
		}
		if name := strings.TrimSpace(au.Name); name != "" {
			item.Credits = append(item.Credits, providers.Credit{Name: name, Role: db.CreditRoleAuthor})
		}
	}

	var eds struct {
		Entries []edition `json:"entries"`
	}
	if err := p.http.GetJSON(ctx, p.BaseURL+"/works/"+externalID+"/editions.json?limit="+strconv.Itoa(maxEditions), nil, &eds); err != nil {
		return nil, err
	}
	for i, e := range eds.Entries {
		if i >= maxEditions {
			break
		}
		item.Editions = append(item.Editions, toEdition(e, w.Title))
	}
	if item.ReleaseYear == 0 {
		for _, ed := range item.Editions {
			if ed.ReleaseDate != nil && (item.ReleaseYear == 0 || ed.ReleaseDate.Year() < item.ReleaseYear) {
				item.ReleaseYear = ed.ReleaseDate.Year()
			}
		}
	}
	return item, nil
}

func toEdition(e edition, workTitle string) providers.Edition {
	ed := providers.Edition{Format: format(e.PhysicalFmt)}
	if t := strings.TrimSpace(e.Title); t != "" && !strings.EqualFold(t, workTitle) {
		ed.Title = t
	}
	if len(e.Publishers) > 0 {
		ed.Publisher = strings.TrimSpace(e.Publishers[0])
	}
	if t, _, ok := parseDate(e.PublishDate); ok {
		ed.ReleaseDate = &t
	}
	if e.NumberOfPages > 0 {
		n := e.NumberOfPages
		ed.PageCount = &n
	}
	if m := editionKey.FindStringSubmatch(e.Key); m != nil {
		ed.ExternalIDs = append(ed.ExternalIDs, providers.ExternalID{
			Provider: "openlibrary_edition", Value: m[1], URL: "https://openlibrary.org/books/" + m[1],
		})
	}
	if len(e.ISBN13) > 0 && e.ISBN13[0] != "" {
		ed.ExternalIDs = append(ed.ExternalIDs, providers.ExternalID{Provider: "isbn13", Value: e.ISBN13[0]})
	}
	if len(e.ISBN10) > 0 && e.ISBN10[0] != "" {
		ed.ExternalIDs = append(ed.ExternalIDs, providers.ExternalID{Provider: "isbn10", Value: e.ISBN10[0]})
	}
	return ed
}

func workExternalID(id string) providers.ExternalID {
	return providers.ExternalID{Provider: "openlibrary_work", Value: id, URL: "https://openlibrary.org/works/" + id}
}

func coverURL(id int) string {
	if id <= 0 {
		return ""
	}
	return coversBaseURL + "/b/id/" + strconv.Itoa(id) + "-L.jpg"
}

func description(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return strings.TrimSpace(obj.Value)
	}
	return ""
}

var genericSubjects = map[string]bool{
	"fiction": true, "general": true, "accessible book": true, "protected daisy": true,
	"in library": true, "lending library": true, "large type books": true, "classics": true,
}

func normalizeGenres(subjects []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range subjects {
		g := strings.ToLower(strings.TrimSpace(s))
		if g == "" || len(g) > 30 || strings.ContainsAny(g, ":=,") || genericSubjects[g] || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
		if len(out) == maxGenres {
			break
		}
	}
	return out
}

func format(physical string) db.EditionFormat {
	f := strings.ToLower(physical)
	switch {
	case strings.Contains(f, "hardcover"), strings.Contains(f, "hardback"):
		return db.EditionFormatHardcover
	case strings.Contains(f, "paperback"), strings.Contains(f, "mass market"), strings.Contains(f, "softcover"):
		return db.EditionFormatPaperback
	case strings.Contains(f, "ebook"), strings.Contains(f, "e-book"), strings.Contains(f, "kindle"):
		return db.EditionFormatEbook
	case strings.Contains(f, "audio"):
		return db.EditionFormatAudiobook
	}
	return ""
}

var dateLayouts = []string{"2006-01-02", "Jan 02, 2006", "January 2, 2006", "Jan 2, 2006", "2 January 2006", "02/01/2006"}

// parseDate reads Open Library's free-form dates; full is false when only a year was found.
func parseDate(s string) (t time.Time, full bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, false
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true, true
		}
	}
	if m := yearRe.FindString(s); m != "" {
		y, _ := strconv.Atoi(m)
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), false, true
	}
	return time.Time{}, false, false
}

var yearRe = regexp.MustCompile(`\b(1[5-9]|20)\d{2}\b`)

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
