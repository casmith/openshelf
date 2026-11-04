package discover

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/providers"
)

// Per-call provider deadlines. Variables so tests can shorten them.
var (
	SearchTimeout = 8 * time.Second
	FetchTimeout  = 30 * time.Second
)

type providerResults struct {
	Provider string
	Results  []result
	Notice   string
}

type result struct {
	providers.SearchResult
	ItemID *uuid.UUID // set when already in the catalog
}

// searchProviders queries every configured provider for kind concurrently. A provider
// that fails or exceeds SearchTimeout yields a notice instead of results.
func searchProviders(ctx context.Context, d *deps.Deps, kind db.MediaKind, query string) ([]providerResults, error) {
	ps := d.Providers.For(kind)
	out := make([]providerResults, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = searchOne(ctx, d, p, kind, query)
		}()
	}
	wg.Wait()
	return out, markImported(ctx, d, out)
}

func searchOne(ctx context.Context, d *deps.Deps, p providers.Provider, kind db.MediaKind, query string) providerResults {
	pr := providerResults{Provider: p.Name()}
	ctx, cancel := context.WithTimeout(ctx, SearchTimeout)
	defer cancel()
	type reply struct {
		res []providers.SearchResult
		err error
	}
	// Run in a goroutine so a provider that ignores ctx still can't block the page.
	ch := make(chan reply, 1)
	go func() {
		res, err := p.Search(ctx, kind, query)
		ch <- reply{res, err}
	}()
	var rep reply
	select {
	case rep = <-ch:
	case <-ctx.Done():
		rep.err = ctx.Err()
	}
	recordResult(p.Name(), rep.err, d.Now())
	if rep.err != nil {
		d.Log.Warn("discover: provider search failed", "provider", p.Name(), "err", rep.err)
		if errors.Is(rep.err, context.DeadlineExceeded) {
			pr.Notice = p.Name() + " took too long to respond."
		} else {
			pr.Notice = p.Name() + " is unavailable right now."
		}
		return pr
	}
	for _, r := range rep.res {
		if r.ExternalID.Value == "" || r.Title == "" {
			continue
		}
		pr.Results = append(pr.Results, result{SearchResult: r})
	}
	return pr
}

func markImported(ctx context.Context, d *deps.Deps, groups []providerResults) error {
	var provs, vals []string
	for _, g := range groups {
		for _, r := range g.Results {
			provs = append(provs, r.ExternalID.Provider)
			vals = append(vals, r.ExternalID.Value)
		}
	}
	if len(provs) == 0 {
		return nil
	}
	rows, err := d.Q.DiscoverFindItemsByExternalIDs(ctx, db.DiscoverFindItemsByExternalIDsParams{Providers: provs, Vals: vals})
	if err != nil {
		return err
	}
	known := map[[2]string]uuid.UUID{}
	for _, r := range rows {
		known[[2]string{r.Provider, r.Value}] = r.PublicID
	}
	for gi := range groups {
		for ri := range groups[gi].Results {
			r := &groups[gi].Results[ri]
			if id, ok := known[[2]string{r.ExternalID.Provider, r.ExternalID.Value}]; ok {
				r.ItemID = &id
			}
		}
	}
	return nil
}

type providerStatus struct {
	LastError   string
	LastErrorAt time.Time
	LastOKAt    time.Time
}

var statuses = struct {
	sync.Mutex
	m map[string]providerStatus
}{m: map[string]providerStatus{}}

func recordResult(name string, err error, now time.Time) {
	statuses.Lock()
	defer statuses.Unlock()
	s := statuses.m[name]
	if err != nil && !errors.Is(err, providers.ErrNotFound) {
		s.LastError, s.LastErrorAt = err.Error(), now
	} else {
		s.LastOKAt = now
	}
	statuses.m[name] = s
}

func statusFor(name string) providerStatus {
	statuses.Lock()
	defer statuses.Unlock()
	return statuses.m[name]
}
