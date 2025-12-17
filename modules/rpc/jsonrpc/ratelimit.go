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

package jsonrpc

import (
	"net"
	"net/http"
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
}

// DefaultRateLimitConfig returns a default rate limit configuration.
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		RequestsPerSecond: 100,
		BurstSize:         200,
		CleanupInterval:   time.Minute,
		EntryTTL:          time.Minute * 5,
	}
}

// rateLimitEntry tracks rate limiting state for a single IP.
type rateLimitEntry struct {
	tokens     float64
	lastUpdate time.Time
}

// RateLimiter implements a token bucket rate limiter for HTTP requests.
type RateLimiter struct {
	config  *RateLimitConfig
	entries map[string]*rateLimitEntry
	mu      sync.Mutex
	stopCh  chan struct{}
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}
	rl := &RateLimiter{
		config:  config,
		entries: make(map[string]*rateLimitEntry),
		stopCh:  make(chan struct{}),
	}
	go rl.cleanup()
	return rl
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

// getClientIP extracts the client IP from the request.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxied requests)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// X-Forwarded-For may contain multiple IPs, use the first one
		if ip := net.ParseIP(xff); ip != nil {
			return ip.String()
		}
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimitMiddleware creates an HTTP middleware that applies rate limiting.
func RateLimitMiddleware(rl *RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
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
		ip := getClientIP(r)
		if !rl.Allow(ip) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		handler(w, r)
	}
}

