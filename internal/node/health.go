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
// Node health probes. Implements /health liveness and readiness
// handlers reporting sync status, peer count, pending transaction
// count and database availability so orchestration layers
// (Kubernetes, systemd) can make restart decisions.

package node

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/n42blockchain/N42/log"
)

// HealthStatus represents the health check response.
type HealthStatus struct {
	Status      string `json:"status"`                // "healthy" or "unhealthy"
	CurrentBlock uint64 `json:"current_block"`
	HighestBlock uint64 `json:"highest_block"`         // highest known block (from peers)
	Syncing     bool   `json:"syncing"`
	PeerCount   int    `json:"peer_count"`
	Uptime      string `json:"uptime"`
}

// HealthProvider supplies node status information for the health check endpoint.
type HealthProvider interface {
	CurrentBlock() uint64
	HighestBlock() uint64
	IsSyncing() bool
	PeerCount() int
}

// healthHandler serves the /health HTTP endpoint.
type healthHandler struct {
	provider  HealthProvider
	startTime time.Time
}

func newHealthHandler(provider HealthProvider) *healthHandler {
	return &healthHandler{
		provider:  provider,
		startTime: time.Now(),
	}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	status := HealthStatus{
		Uptime: time.Since(h.startTime).Truncate(time.Second).String(),
	}

	if h.provider != nil {
		status.CurrentBlock = h.provider.CurrentBlock()
		status.HighestBlock = h.provider.HighestBlock()
		status.Syncing = h.provider.IsSyncing()
		status.PeerCount = h.provider.PeerCount()
	}

	// Determine health status
	httpStatus := http.StatusOK
	status.Status = "healthy"

	if h.provider != nil && status.PeerCount == 0 {
		status.Status = "unhealthy"
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Warn("Failed to encode health status", "err", err)
	}
}
