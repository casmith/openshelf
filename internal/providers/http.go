package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const UserAgent = "OpenShelf/0.1 (+https://github.com/casmith/openshelf)"

// HTTP is a rate-limited client with retry on 429/5xx for talking to fixed provider hosts.
// Use internal/safehttp for arbitrary user-supplied URLs.
type HTTP struct {
	Client     *http.Client
	Limiter    *rate.Limiter
	UserAgent  string
	MaxRetries int
}

func NewHTTP(perSecond float64, burst int) *HTTP {
	return &HTTP{
		Client:     &http.Client{Timeout: 15 * time.Second},
		Limiter:    rate.NewLimiter(rate.Limit(perSecond), burst),
		UserAgent:  UserAgent,
		MaxRetries: 3,
	}
}

type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string { return fmt.Sprintf("provider http %d: %s", e.Status, e.Body) }

// Do sends req, retrying 429 and 5xx with backoff (honoring Retry-After). Requests
// with bodies must set GetBody (http.NewRequest does for common body types).
func (h *HTTP) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", h.UserAgent)
	}
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		if h.Limiter != nil {
			if err := h.Limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		res, err := h.Client.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusTooManyRequests && res.StatusCode < 500 {
			return res, nil
		}
		wait := backoff
		if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		res.Body.Close()
		if attempt >= h.MaxRetries {
			return nil, &StatusError{Status: res.StatusCode, Body: string(body)}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

// GetJSON decodes a successful JSON response into out. 404 maps to ErrNotFound.
func (h *HTTP) GetJSON(ctx context.Context, url string, header http.Header, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	return h.DoJSON(ctx, req, out)
}

func (h *HTTP) DoJSON(ctx context.Context, req *http.Request, out any) error {
	res, err := h.Do(ctx, req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return &StatusError{Status: res.StatusCode, Body: string(body)}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 10<<20)).Decode(out)
}
