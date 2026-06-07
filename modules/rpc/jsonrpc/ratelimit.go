// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Per-IP HTTP rate limiter middleware.
// RateLimitConfig carries RequestsPerSecond, BurstSize, CleanupInterval
// and EntryTTL; DefaultRateLimitConfig supplies sane defaults.
// The limiter maintains a map of token buckets keyed by client IP,
// background-evicts expired entries, and wraps an http.Handler to
// reject over-quota requests with HTTP 429.

package jsonrpc

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimitConfig defines the rate limiting configuration.
type RateLimitConfig struct {
	// RequestsPerSecond is the maximum number of requests per second per IP.
	RequestsPerSecond int
	// BurstSize is the maximum burst size for rate limiting.
	BurstSize int
	// CleanupInterval is how often to clean up expired entries.
	CleanupInterval time.Duration
	// EntryTTL is how long to keep entries in the rate limiter.
	EntryTTL time.Duration
	// TrustedProxies is the CIDR allowlist of reverse-proxy / CDN egress ranges.
	// X-Forwarded-For / X-Real-IP are honored ONLY when the immediate peer
	// (RemoteAddr) is in this set; otherwise the limiter keys on RemoteAddr.
	// Empty (default) = never trust forwarded headers (secure by default), so a
	// client cannot spoof its rate-limit identity by setting X-Forwarded-For.
	TrustedProxies []string
}

// DefaultRateLimitConfig returns a default rate limit configuration.
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		RequestsPerSecond: 100,
		BurstSize:         200,
		CleanupInterval:   time.Minute,
		EntryTTL:          5 * time.Minute,
	}
}

// rateLimitEntry tracks rate limiting state for a single IP.
type rateLimitEntry struct {
	tokens     float64
	lastUpdate time.Time
}

// RateLimiter implements a token bucket rate limiter for HTTP requests.
type RateLimiter struct {
	config      *RateLimitConfig
	entries     map[string]*rateLimitEntry
	trustedNets []*net.IPNet // parsed config.TrustedProxies
	mu          sync.Mutex
	stopCh      chan struct{}
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}
	// Fill in defaults for zero-valued fields
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = time.Minute
	}
	if config.EntryTTL <= 0 {
		config.EntryTTL = 5 * time.Minute
	}
	if config.BurstSize <= 0 {
		config.BurstSize = config.RequestsPerSecond * 2
	}
	rl := &RateLimiter{
		config:      config,
		entries:     make(map[string]*rateLimitEntry),
		trustedNets: ParseCIDRs(config.TrustedProxies),
		stopCh:      make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// clientIP resolves the rate-limit key for r honoring the configured trusted
// proxies (see ClientIP).
func (rl *RateLimiter) clientIP(r *http.Request) string {
	return ClientIP(r, rl.trustedNets)
}

// Stop stops the rate limiter's cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// cleanup periodically removes expired entries.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, entry := range rl.entries {
				if now.Sub(entry.lastUpdate) > rl.config.EntryTTL {
					delete(rl.entries, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

// Allow checks if a request from the given IP is allowed.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	entry, exists := rl.entries[ip]

	if !exists {
		rl.entries[ip] = &rateLimitEntry{
			tokens:     float64(rl.config.BurstSize) - 1,
			lastUpdate: now,
		}
		return true
	}

	// Refill tokens based on time elapsed
	elapsed := now.Sub(entry.lastUpdate).Seconds()
	entry.tokens += elapsed * float64(rl.config.RequestsPerSecond)
	if entry.tokens > float64(rl.config.BurstSize) {
		entry.tokens = float64(rl.config.BurstSize)
	}
	entry.lastUpdate = now

	if entry.tokens >= 1 {
		entry.tokens--
		return true
	}
	return false
}

// ParseCIDRs parses a list of CIDR strings into nets, silently dropping invalid
// entries (an unparseable proxy CIDR simply won't be trusted — fail closed).
func ParseCIDRs(cidrs []string) []*net.IPNet {
	if len(cidrs) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Accept a bare IP as a /32 or /128.
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				c = fmt.Sprintf("%s/%d", c, bits)
			}
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteHost returns the host portion of r.RemoteAddr (no port).
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIP returns the client IP to use as a rate-limit / bandwidth key. It
// trusts X-Forwarded-For / X-Real-IP ONLY when the immediate peer (RemoteAddr)
// is one of trustedProxies; otherwise it returns RemoteAddr. This is secure by
// default: with no trusted proxies a client cannot spoof its identity (and thus
// bypass per-IP limits or balloon the limiter maps) by forging X-Forwarded-For.
//
// When the peer is trusted, it returns the right-most X-Forwarded-For entry that
// is NOT itself a trusted proxy — i.e. the real client as seen by the first
// trusted hop, which the client cannot forge (a spoofed left-most entry is
// skipped over).
func ClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	host := remoteHost(r)
	if len(trustedProxies) == 0 || !ipInNets(net.ParseIP(host), trustedProxies) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil && !ipInNets(ip, trustedProxies) {
				return ip.String()
			}
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil {
			return ip.String()
		}
	}
	return host
}

// RateLimitMiddleware creates an HTTP middleware that applies rate limiting.
func RateLimitMiddleware(rl *RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := rl.clientIP(r)
		if !rl.Allow(ip) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitHandlerFunc wraps an http.HandlerFunc with rate limiting.
func RateLimitHandlerFunc(rl *RateLimiter, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := rl.clientIP(r)
		if !rl.Allow(ip) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		handler(w, r)
	}
}
