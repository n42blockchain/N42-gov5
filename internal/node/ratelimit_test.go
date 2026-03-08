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

package node

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestRateLimitMiddlewareIntegration(t *testing.T) {
	// Create a simple handler that always returns 200
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Create rate limiter with burst of 3
	rl := jsonrpc.NewRateLimiter(&jsonrpc.RateLimitConfig{
		RequestsPerSecond: 1,
		BurstSize:         3,
	})
	defer rl.Stop()

	// Wrap with rate limiter via the HTTP handler stack
	handler := NewHTTPHandlerStack(inner, nil, []string{"*"}, nil, rl)

	// First 3 requests should succeed (burst)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// 4th request should be rate limited
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	t.Logf("Rate limit response: %s", string(body))
}

func TestRateLimitPerIP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rl := jsonrpc.NewRateLimiter(&jsonrpc.RateLimitConfig{
		RequestsPerSecond: 1,
		BurstSize:         2,
	})
	defer rl.Stop()

	handler := NewHTTPHandlerStack(inner, nil, []string{"*"}, nil, rl)

	// Exhaust burst for IP1
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("IP1 request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// IP1 is now rate limited
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("IP1 should be rate limited, got %d", rr.Code)
	}

	// IP2 should still be allowed
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.RemoteAddr = "10.0.0.2:1234"
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("IP2 should be allowed, got %d", rr2.Code)
	}
}

func TestNoRateLimitWhenNil(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// No rate limiter
	handler := NewHTTPHandlerStack(inner, nil, []string{"*"}, nil, nil)

	// Many requests should all succeed
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}
}
