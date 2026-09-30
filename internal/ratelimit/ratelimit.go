// Package ratelimit throttles requests per client address (rest-api.md §1: login and registration allow 10
// requests per minute per address). Limits are kept in memory, so each service instance counts on its own.
package ratelimit

import (
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
)

// Limiter allows each key a burst of limit requests, then one more every period/limit. It implements the
// generic cell rate algorithm with integer durations, so a client that waits the advertised Retry-After is
// always let through.
type Limiter struct {
	mu        sync.Mutex
	interval  time.Duration // period / limit: the time one request takes to be regained
	tolerance time.Duration // interval * (limit - 1): how far a burst may run ahead of the steady rate
	period    time.Duration
	now       func() time.Time
	// arrivals holds each key's theoretical arrival time: when its next request would conform at the
	// steady rate.
	arrivals  map[string]time.Time
	lastSweep time.Time
}

// New returns a limiter that allows limit requests per period for each key.
func New(limit int, period time.Duration, now func() time.Time) *Limiter {
	interval := period / time.Duration(limit)
	return &Limiter{
		interval:  interval,
		tolerance: interval * time.Duration(limit-1),
		period:    period,
		now:       now,
		arrivals:  map[string]time.Time{},
		lastSweep: now(),
	}
}

// Allow spends one request of key. When none is left, it reports how long until one is.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)
	arrival, found := l.arrivals[key]
	if !found || arrival.Before(now) {
		arrival = now
	}
	if allowedAt := arrival.Add(-l.tolerance); now.Before(allowedAt) {
		return false, allowedAt.Sub(now)
	}
	l.arrivals[key] = arrival.Add(l.interval)
	return true, 0
}

// sweep forgets keys that have regained their whole burst, so memory follows the number of recent clients. It
// runs at most once per period.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.period {
		return
	}
	for key, arrival := range l.arrivals {
		if !arrival.After(now) {
			delete(l.arrivals, key)
		}
	}
	l.lastSweep = now
}

// Keys returns how many keys the limiter tracks.
func (l *Limiter) Keys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.arrivals)
}

// Middleware rejects requests over the limit with 429 RATE_LIMITED and a Retry-After header in whole seconds.
func (l *Limiter) Middleware(clientIP func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := l.Allow(clientIP(r))
			if !ok {
				seconds := int(math.Ceil(retryAfter.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
				httpx.WriteProblem(w, r, httpx.NewProblem(http.StatusTooManyRequests, httpx.CodeRateLimited,
					"too many requests; retry later"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP finds the address of the client behind trusted proxies.
type ClientIP struct {
	trusted []netip.Prefix
}

// NewClientIP parses a comma-separated list of CIDR ranges whose X-Forwarded-For headers are trusted.
func NewClientIP(trustedProxies string) (*ClientIP, error) {
	c := &ClientIP{}
	for cidr := range strings.SplitSeq(trustedProxies, ",") {
		if cidr = strings.TrimSpace(cidr); cidr == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", cidr, err)
		}
		c.trusted = append(c.trusted, prefix.Masked())
	}
	return c, nil
}

// Of returns the client address of r. A request from a trusted proxy is attributed to the nearest address in
// X-Forwarded-For that is not a trusted proxy, reading from the right, because the left part can be forged.
func (c *ClientIP) Of(r *http.Request) string {
	remote := hostOf(r.RemoteAddr)
	if !c.isTrusted(remote) {
		return remote
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for hop := range strings.SplitSeq(header, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !c.isTrusted(hops[i]) {
			return hops[i]
		}
	}
	if len(hops) > 0 {
		return hops[0]
	}
	return remote
}

func (c *ClientIP) isTrusted(address string) bool {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range c.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func hostOf(remoteAddr string) string {
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return addrPort.Addr().Unmap().String()
	}
	return remoteAddr
}
