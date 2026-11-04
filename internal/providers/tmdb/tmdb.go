package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

const (
	DefaultBaseURL = "https://api.themoviedb.org/3"
	Attribution    = "This product uses the TMDB API but is not endorsed or certified by TMDB."
	posterSize     = "w500"
	maxCast        = 5
)

type Provider struct {
	BaseURL string
	HTTP    *providers.HTTP
	token   string

	mu        sync.Mutex
	imageBase string
}

func New(cfg config.Config) *Provider {
	return &Provider{BaseURL: DefaultBaseURL, HTTP: providers.NewHTTP(20, 20), token: cfg.TMDBToken}
}

func (p *Provider) Name() string          { return "tmdb" }
func (p *Provider) Kinds() []db.MediaKind { return []db.MediaKind{db.MediaKindFilm, db.MediaKindShow} }
func (p *Provider) Configured() bool      { return p.token != "" }

func (p *Provider) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(p.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+p.token)
	return p.HTTP.GetJSON(ctx, u, h, out)
}

func (p *Provider) posterURL(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.imageBase == "" {
		var cfg struct {
			Images struct {
				SecureBaseURL string `json:"secure_base_url"`
			} `json:"images"`
		}
		if err := p.get(ctx, "/configuration", nil, &cfg); err != nil {
			return "", fmt.Errorf("tmdb configuration: %w", err)
		}
		if cfg.Images.SecureBaseURL == "" {
			return "", fmt.Errorf("tmdb configuration: missing images.secure_base_url")
		}
		p.imageBase = strings.TrimRight(cfg.Images.SecureBaseURL, "/") + "/"
	}
	return p.imageBase + posterSize + path, nil
}

func endpoint(kind db.MediaKind) (string, string, error) {
	switch kind {
	case db.MediaKindFilm:
		return "movie", "tmdb_movie", nil
	case db.MediaKindShow:
		return "tv", "tmdb_tv", nil
	}
	return "", "", fmt.Errorf("tmdb: unsupported kind %q", kind)
}

func extID(provider, id string) providers.ExternalID {
	x := providers.ExternalID{Provider: provider, Value: id}
	switch provider {
	case "tmdb_movie":
		x.URL = "https://www.themoviedb.org/movie/" + id
	case "tmdb_tv":
		x.URL = "https://www.themoviedb.org/tv/" + id
	case "imdb":
		x.URL = "https://www.imdb.com/title/" + id + "/"
	}
	return x
}

type searchResult struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	PosterPath    string `json:"poster_path"`
}

func (p *Provider) Search(ctx context.Context, kind db.MediaKind, query string) ([]providers.SearchResult, error) {
	ep, provider, err := endpoint(kind)
	if err != nil {
		return nil, err
	}
	var res struct {
		Results []searchResult `json:"results"`
	}
	if err := p.get(ctx, "/search/"+ep, url.Values{"query": {query}}, &res); err != nil {
		return nil, err
	}
	out := make([]providers.SearchResult, 0, len(res.Results))
	for _, r := range res.Results {
		title, orig, date := r.Title, r.OriginalTitle, r.ReleaseDate
		if kind == db.MediaKindShow {
			title, orig, date = r.Name, r.OriginalName, r.FirstAirDate
		}
		if title == "" {
			continue
		}
		cover, err := p.posterURL(ctx, r.PosterPath)
		if err != nil {
			return nil, err
		}
		_, year := parseDate(date)
		sub := ""
		if orig != "" && orig != title {
			sub = orig
		} else if year > 0 {
			sub = strconv.Itoa(year)
		}
		out = append(out, providers.SearchResult{
			Provider:   p.Name(),
			ExternalID: extID(provider, strconv.FormatInt(r.ID, 10)),
			Kind:       kind,
			Title:      title,
			Year:       year,
			Subtitle:   sub,
			CoverURL:   cover,
		})
	}
	return out, nil
}

type person struct {
	Name string `json:"name"`
	Job  string `json:"job"`
}

type named struct {
	Name string `json:"name"`
}

type details struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	Name          string   `json:"name"`
	OriginalTitle string   `json:"original_title"`
	OriginalName  string   `json:"original_name"`
	ReleaseDate   string   `json:"release_date"`
	FirstAirDate  string   `json:"first_air_date"`
	Overview      string   `json:"overview"`
	PosterPath    string   `json:"poster_path"`
	Runtime       *int32   `json:"runtime"`
	Status        string   `json:"status"`
	Seasons       *int32   `json:"number_of_seasons"`
	Episodes      *int32   `json:"number_of_episodes"`
	Genres        []named  `json:"genres"`
	Networks      []named  `json:"networks"`
	CreatedBy     []person `json:"created_by"`
	Credits       struct {
		Cast []person `json:"cast"`
		Crew []person `json:"crew"`
	} `json:"credits"`
	ExternalIDs struct {
		IMDbID string `json:"imdb_id"`
	} `json:"external_ids"`
}

func (p *Provider) Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*providers.Item, error) {
	ep, provider, err := endpoint(kind)
	if err != nil {
		return nil, err
	}
	if _, err := strconv.ParseInt(externalID, 10, 64); err != nil {
		return nil, providers.ErrNotFound
	}
	var d details
	q := url.Values{"append_to_response": {"credits,external_ids"}}
	if err := p.get(ctx, "/"+ep+"/"+externalID, q, &d); err != nil {
		return nil, err
	}
	cover, err := p.posterURL(ctx, d.PosterPath)
	if err != nil {
		return nil, err
	}
	item := &providers.Item{
		Kind:        kind,
		Synopsis:    d.Overview,
		CoverURL:    cover,
		Attribution: Attribution,
		ExternalIDs: []providers.ExternalID{extID(provider, strconv.FormatInt(d.ID, 10))},
		Credits:     credits(d),
	}
	if d.ExternalIDs.IMDbID != "" {
		item.ExternalIDs = append(item.ExternalIDs, extID("imdb", d.ExternalIDs.IMDbID))
	}
	for _, g := range d.Genres {
		if n := strings.ToLower(strings.TrimSpace(g.Name)); n != "" {
			item.Genres = append(item.Genres, n)
		}
	}
	date := d.ReleaseDate
	if kind == db.MediaKindFilm {
		item.Title, item.OriginalTitle = d.Title, d.OriginalTitle
		item.Film = &providers.FilmDetails{RuntimeMinutes: positive(d.Runtime)}
	} else {
		item.Title, item.OriginalTitle = d.Name, d.OriginalName
		date = d.FirstAirDate
		item.Show = &providers.ShowDetails{
			Status:       normalizeStatus(d.Status),
			SeasonCount:  d.Seasons,
			EpisodeCount: d.Episodes,
		}
		if len(d.Networks) > 0 {
			item.Show.Network = d.Networks[0].Name
		}
	}
	if item.OriginalTitle == item.Title {
		item.OriginalTitle = ""
	}
	item.ReleaseDate, item.ReleaseYear = parseDate(date)
	return item, nil
}

func credits(d details) []providers.Credit {
	var out []providers.Credit
	seen := map[providers.Credit]bool{}
	add := func(name string, role db.CreditRole) {
		c := providers.Credit{Name: strings.TrimSpace(name), Role: role}
		if c.Name == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, c := range d.CreatedBy {
		add(c.Name, db.CreditRoleCreator)
	}
	for _, c := range d.Credits.Crew {
		if c.Job == "Director" {
			add(c.Name, db.CreditRoleDirector)
		}
	}
	for _, c := range d.Credits.Crew {
		if c.Job == "Screenplay" || c.Job == "Writer" {
			add(c.Name, db.CreditRoleWriter)
		}
	}
	for i, c := range d.Credits.Cast {
		if i >= maxCast {
			break
		}
		add(c.Name, db.CreditRoleActor)
	}
	return out
}

// normalizeStatus maps TMDB's status strings to the show_details vocabulary.
func normalizeStatus(s string) string {
	switch s {
	case "Returning Series":
		return "returning"
	case "Cancelled":
		return "canceled"
	}
	return strings.ToLower(s)
}

func positive(n *int32) *int32 {
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

func parseDate(s string) (*time.Time, int) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, 0
	}
	return &t, t.Year()
}
