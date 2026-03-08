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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockHealthProvider implements HealthProvider for testing.
type mockHealthProvider struct {
	currentBlock uint64
	highestBlock uint64
	syncing      bool
	peerCount    int
}

func (m *mockHealthProvider) CurrentBlock() uint64 { return m.currentBlock }
func (m *mockHealthProvider) HighestBlock() uint64 { return m.highestBlock }
func (m *mockHealthProvider) IsSyncing() bool      { return m.syncing }
func (m *mockHealthProvider) PeerCount() int       { return m.peerCount }

func TestHealthHandler_Healthy(t *testing.T) {
	provider := &mockHealthProvider{
		currentBlock: 100,
		highestBlock: 105,
		syncing:      false,
		peerCount:    5,
	}
	handler := newHealthHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var status HealthStatus
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}

	if status.Status != "healthy" {
		t.Errorf("expected healthy, got %s", status.Status)
	}
	if status.CurrentBlock != 100 {
		t.Errorf("expected block 100, got %d", status.CurrentBlock)
	}
	if status.PeerCount != 5 {
		t.Errorf("expected 5 peers, got %d", status.PeerCount)
	}
}

func TestHealthHandler_Unhealthy_NoPeers(t *testing.T) {
	provider := &mockHealthProvider{
		currentBlock: 100,
		highestBlock: 100,
		peerCount:    0,
	}
	handler := newHealthHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}

	var status HealthStatus
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}

	if status.Status != "unhealthy" {
		t.Errorf("expected unhealthy, got %s", status.Status)
	}
}

func TestHealthHandler_NilProvider(t *testing.T) {
	handler := newHealthHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with nil provider, got %d", rr.Code)
	}

	var status HealthStatus
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "healthy" {
		t.Errorf("expected healthy with nil provider, got %s", status.Status)
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	handler := newHealthHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}
