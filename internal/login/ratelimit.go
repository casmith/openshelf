package login

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/casmith/openshelf/internal/web"
)

type limiter struct {
	mu      sync.Mutex
	clients map[string]*client
	every   time.Duration
	burst   int
	trusted []netip.Prefix
}

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLimiter(every time.Duration, burst int, trusted []netip.Prefix) *limiter {
	return &limiter{clients: map[string]*client{}, every: every, burst: burst, trusted: trusted}
}

func (l *limiter) allow(r *http.Request) bool {
	ip := web.ClientIP(r, l.trusted)
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clients) > 10000 {
		for k, c := range l.clients {
			if now.Sub(c.seen) > time.Hour {
				delete(l.clients, k)
			}
		}
	}
	c, ok := l.clients[ip]
	if !ok {
		c = &client{lim: rate.NewLimiter(rate.Every(l.every), l.burst)}
		l.clients[ip] = c
	}
	c.seen = now
	return c.lim.Allow()
}
