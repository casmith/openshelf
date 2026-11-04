package login

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type limiter struct {
	mu      sync.Mutex
	clients map[string]*client
	every   time.Duration
	burst   int
}

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLimiter(every time.Duration, burst int) *limiter {
	return &limiter{clients: map[string]*client{}, every: every, burst: burst}
}

func (l *limiter) allow(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
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
