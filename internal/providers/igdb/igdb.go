// Package igdb adapts the IGDB v4 API (Twitch client-credentials auth) to providers.Provider.
package igdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/casmith/openshelf/internal/config"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/providers"
)

const (
	DefaultAPIURL   = "https://api.igdb.com/v4"
	DefaultTokenURL = "https://id.twitch.tv/oauth2/token"
	Attribution     = "Game data provided by IGDB.com"

	coverURL     = "https://images.igdb.com/igdb/image/upload/t_cover_big/%s.jpg"
	tokenLeeway  = 60 * time.Second
	steamSource  = 1
	searchFields = "fields name,first_release_date,cover.image_id,platforms.name; limit 20;"
	fetchFields  = "fields name,url,summary,first_release_date,cover.image_id,genres.name,franchise.name,franchises.name," +
		"involved_companies.developer,involved_companies.publisher,involved_companies.company.name," +
		"release_dates.date,release_dates.region,release_dates.release_region,release_dates.platform.name," +
		"external_games.category,external_games.external_game_source,external_games.uid;"
)

type Provider struct {
	cfg      config.Config
	APIURL   string
	TokenURL string
	HTTP     *providers.HTTP

	now     func() time.Time
	mu      sync.Mutex
	token   string
	expires time.Time
}

func New(cfg config.Config) *Provider {
	return &Provider{
		cfg:      cfg,
		APIURL:   DefaultAPIURL,
		TokenURL: DefaultTokenURL,
		HTTP:     providers.NewHTTP(4, 4),
		now:      time.Now,
	}
}

func (p *Provider) Name() string          { return "igdb" }
func (p *Provider) Kinds() []db.MediaKind { return []db.MediaKind{db.MediaKindGame} }
func (p *Provider) Configured() bool {
	return p.cfg.IGDBClientID != "" && p.cfg.IGDBClientSecret != ""
}

type named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type game struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	URL              string  `json:"url"`
	Summary          string  `json:"summary"`
	FirstReleaseDate *int64  `json:"first_release_date"`
	Cover            *cover  `json:"cover"`
	Platforms        []named `json:"platforms"`
	Genres           []named `json:"genres"`
	Franchise        *named  `json:"franchise"`
	Franchises       []named `json:"franchises"`
	InvolvedCompany  []struct {
		Developer bool  `json:"developer"`
		Publisher bool  `json:"publisher"`
		Company   named `json:"company"`
	} `json:"involved_companies"`
	ReleaseDates []struct {
		Date          *int64 `json:"date"`
		Region        int    `json:"region"`
		ReleaseRegion int    `json:"release_region"`
		Platform      *named `json:"platform"`
	} `json:"release_dates"`
	ExternalGames []struct {
		Category           int    `json:"category"`
		ExternalGameSource int    `json:"external_game_source"`
		UID                string `json:"uid"`
	} `json:"external_games"`
}

type cover struct {
	ImageID string `json:"image_id"`
}

func (p *Provider) Search(ctx context.Context, kind db.MediaKind, query string) ([]providers.SearchResult, error) {
	if kind != db.MediaKindGame || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	var games []game
	body := fmt.Sprintf("search \"%s\"; %s", escape(query), searchFields)
	if err := p.query(ctx, "games", body, &games); err != nil {
		return nil, err
	}
	out := make([]providers.SearchResult, 0, len(games))
	for _, g := range games {
		if g.Name == "" {
			continue
		}
		names := make([]string, 0, len(g.Platforms))
		for _, pl := range g.Platforms {
			if pl.Name != "" {
				names = append(names, pl.Name)
			}
		}
		out = append(out, providers.SearchResult{
			Provider:   p.Name(),
			ExternalID: providers.ExternalID{Provider: "igdb_game", Value: strconv.FormatInt(g.ID, 10), URL: g.URL},
			Kind:       db.MediaKindGame,
			Title:      g.Name,
			Year:       year(g.FirstReleaseDate),
			Subtitle:   strings.Join(names, ", "),
			CoverURL:   coverFor(g.Cover),
		})
	}
	return out, nil
}

func (p *Provider) Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*providers.Item, error) {
	id, err := strconv.ParseInt(externalID, 10, 64)
	if kind != db.MediaKindGame || err != nil || id <= 0 {
		return nil, providers.ErrNotFound
	}
	var games []game
	if err := p.query(ctx, "games", fmt.Sprintf("%s where id = %d;", fetchFields, id), &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, providers.ErrNotFound
	}
	return toItem(games[0]), nil
}

func toItem(g game) *providers.Item {
	item := &providers.Item{
		Kind:        db.MediaKindGame,
		Title:       g.Name,
		Synopsis:    strings.TrimSpace(g.Summary),
		CoverURL:    coverFor(g.Cover),
		ReleaseYear: year(g.FirstReleaseDate),
		Attribution: Attribution,
		ExternalIDs: []providers.ExternalID{{Provider: "igdb_game", Value: strconv.FormatInt(g.ID, 10), URL: g.URL}},
	}
	if g.FirstReleaseDate != nil {
		t := time.Unix(*g.FirstReleaseDate, 0).UTC()
		item.ReleaseDate = &t
	}
	for _, gn := range g.Genres {
		if s := strings.ToLower(strings.TrimSpace(gn.Name)); s != "" {
			item.Genres = append(item.Genres, s)
		}
	}
	for _, ic := range g.InvolvedCompany {
		name := strings.TrimSpace(ic.Company.Name)
		if name == "" {
			continue
		}
		if ic.Developer {
			item.Credits = append(item.Credits, providers.Credit{Name: name, Role: db.CreditRoleDeveloper})
		}
		if ic.Publisher {
			item.Credits = append(item.Credits, providers.Credit{Name: name, Role: db.CreditRolePublisher})
		}
	}
	franchise := ""
	if g.Franchise != nil {
		franchise = g.Franchise.Name
	} else if len(g.Franchises) > 0 {
		franchise = g.Franchises[0].Name
	}
	item.Game = &providers.GameDetails{Franchise: franchise}
	item.Editions = editions(g)
	seen := map[string]bool{}
	for _, eg := range g.ExternalGames {
		if (eg.ExternalGameSource == steamSource || eg.Category == steamSource) && eg.UID != "" && !seen[eg.UID] {
			seen[eg.UID] = true
			item.ExternalIDs = append(item.ExternalIDs, providers.ExternalID{
				Provider: "steam_app", Value: eg.UID, URL: "https://store.steampowered.com/app/" + eg.UID,
			})
		}
	}
	return item
}

// editions collapses release dates into one edition per (platform, region), keeping the earliest date.
func editions(g game) []providers.Edition {
	type key struct {
		platform int64
		region   string
	}
	idx := map[key]int{}
	var out []providers.Edition
	for _, rd := range g.ReleaseDates {
		if rd.Platform == nil || rd.Platform.ID == 0 {
			continue
		}
		r := rd.ReleaseRegion
		if r == 0 {
			r = rd.Region
		}
		k := key{rd.Platform.ID, regionName(r)}
		var date *time.Time
		if rd.Date != nil {
			t := time.Unix(*rd.Date, 0).UTC()
			date = &t
		}
		if i, ok := idx[k]; ok {
			if date != nil && (out[i].ReleaseDate == nil || date.Before(*out[i].ReleaseDate)) {
				out[i].ReleaseDate = date
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, providers.Edition{
			Format:      formatFor(rd.Platform.ID),
			Platform:    platformRef(*rd.Platform),
			Region:      k.region,
			ReleaseDate: date,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].ReleaseDate, out[j].ReleaseDate
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return a.Before(*b)
	})
	return out
}

func platformRef(pl named) *providers.PlatformRef {
	id := strconv.FormatInt(pl.ID, 10)
	name := pl.Name
	if slug, ok := platformSlugs[pl.ID]; ok {
		name = slug
	}
	return &providers.PlatformRef{
		ExternalID: &providers.ExternalID{Provider: "igdb_platform", Value: id},
		Name:       name,
	}
}

func formatFor(platformID int64) db.EditionFormat {
	if platformID == platformPC {
		return db.EditionFormatDigital
	}
	return ""
}

// regionName maps IGDB release regions (legacy enum and release_date_regions IDs share values).
func regionName(r int) string {
	switch r {
	case 1, 3, 4:
		return "PAL"
	case 2:
		return "NTSC-U"
	case 5:
		return "JP"
	case 6:
		return "CN"
	case 7:
		return "Asia"
	case 9:
		return "KR"
	case 10:
		return "BR"
	}
	return ""
}

func (p *Provider) query(ctx context.Context, endpoint, body string, out any) error {
	if !p.Configured() {
		return errors.New("igdb: client ID and secret are not configured")
	}
	for attempt := 0; ; attempt++ {
		token, err := p.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(p.APIURL, "/")+"/"+endpoint, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Client-ID", p.cfg.IGDBClientID)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "text/plain")
		err = p.HTTP.DoJSON(ctx, req, out)
		var se *providers.StatusError
		if attempt == 0 && errors.As(err, &se) && se.Status == http.StatusUnauthorized {
			p.invalidate(token)
			continue
		}
		return err
	}
}

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && p.now().Before(p.expires) {
		return p.token, nil
	}
	q := url.Values{
		"client_id":     {p.cfg.IGDBClientID},
		"client_secret": {p.cfg.IGDBClientSecret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequest(http.MethodPost, p.TokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := p.HTTP.DoJSON(ctx, req, &res); err != nil {
		return "", fmt.Errorf("igdb token: %w", err)
	}
	if res.AccessToken == "" {
		return "", errors.New("igdb token: empty access_token")
	}
	p.token = res.AccessToken
	p.expires = p.now().Add(time.Duration(res.ExpiresIn)*time.Second - tokenLeeway)
	return p.token, nil
}

func (p *Provider) invalidate(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == token {
		p.token = ""
	}
}

func escape(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func year(ts *int64) int {
	if ts == nil {
		return 0
	}
	return time.Unix(*ts, 0).UTC().Year()
}

func coverFor(c *cover) string {
	if c == nil || c.ImageID == "" {
		return ""
	}
	return fmt.Sprintf(coverURL, c.ImageID)
}
