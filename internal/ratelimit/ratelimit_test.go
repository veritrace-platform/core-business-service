package ratelimit_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/ratelimit"
)

func TestLimiterAllowsABurstThenRefills(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	l := ratelimit.New(10, time.Minute, func() time.Time { return now })

	for i := range 10 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d denied within the limit", i+1)
		}
	}
	ok, retryAfter := l.Allow("a")
	if ok || retryAfter != 6*time.Second {
		t.Fatalf("11th request: ok = %t, retry after %v; want denied, 6s", ok, retryAfter)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("another key shares the limit")
	}

	now = now.Add(5 * time.Second)
	if ok, retryAfter := l.Allow("a"); ok || retryAfter != time.Second {
		t.Errorf("after 5s: ok = %t, retry after %v; want denied, 1s", ok, retryAfter)
	}
	now = now.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("after 6s: denied, want one request refilled")
	}
}

func TestLimiterForgetsIdleKeys(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	l := ratelimit.New(10, time.Minute, func() time.Time { return now })
	for _, key := range []string{"a", "b", "c"} {
		l.Allow(key)
	}
	if l.Keys() != 3 {
		t.Fatalf("Keys() = %d, want 3", l.Keys())
	}
	now = now.Add(2 * time.Minute)
	l.Allow("d")
	if l.Keys() != 1 {
		t.Errorf("Keys() after the buckets refilled = %d, want 1", l.Keys())
	}
}

func TestMiddleware(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	l := ratelimit.New(1, time.Minute, func() time.Time { return now })
	handler := l.Middleware(func(*http.Request) string { return "client" })(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first request: status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil))
	var p httpx.Problem
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != http.StatusTooManyRequests || p.Code != httpx.CodeRateLimited || rec.Header().Get("Retry-After") != "60" {
		t.Errorf("second request: status = %d, code = %s, Retry-After = %q; want 429 RATE_LIMITED, 60",
			rec.Code, p.Code, rec.Header().Get("Retry-After"))
	}
}

func TestClientIP(t *testing.T) {
	resolver, err := ratelimit.NewClientIP("127.0.0.0/8, 10.0.0.0/8,::1/128")
	if err != nil {
		t.Fatalf("NewClientIP() error = %v", err)
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client", "203.0.113.7:51000", nil, "203.0.113.7"},
		{"direct client forging the header", "203.0.113.7:51000", []string{"198.51.100.1"}, "203.0.113.7"},
		{"behind the gateway", "10.0.0.5:40000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"forged left part", "10.0.0.5:40000", []string{"198.51.100.1, 203.0.113.7"}, "203.0.113.7"},
		{"frontend server then gateway", "10.0.0.5:40000", []string{"203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"several headers", "10.0.0.5:40000", []string{"198.51.100.1", "203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"only proxies", "10.0.0.5:40000", []string{"10.0.0.8, 10.0.0.9"}, "10.0.0.8"},
		{"trusted without header", "127.0.0.1:40000", nil, "127.0.0.1"},
		{"ipv6 loopback", "[::1]:40000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"ipv4-mapped proxy", "[::ffff:10.0.0.5]:40000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"unparseable hop", "10.0.0.5:40000", []string{"unknown"}, "unknown"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.remote
		for _, v := range tt.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		if got := resolver.Of(req); got != tt.want {
			t.Errorf("%s: Of() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNewClientIPRejectsInvalidRanges(t *testing.T) {
	if _, err := ratelimit.NewClientIP("10.0.0.0/8,not-a-cidr"); err == nil {
		t.Error("NewClientIP() accepted an invalid range")
	}
	if c, err := ratelimit.NewClientIP(""); err != nil || c == nil {
		t.Errorf("NewClientIP(\"\") = %v, %v; want a resolver that trusts nobody", c, err)
	}
}
