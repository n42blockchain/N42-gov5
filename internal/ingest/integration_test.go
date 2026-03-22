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

package ingest

import (
	"net"
	"testing"
)

// TestIngestServer_Integration performs an end-to-end smoke test:
// start a real server on a random port, connect a TCP client, send
// multiple batches, and verify pool contents plus server statistics.
func TestIngestServer_Integration(t *testing.T) {
	pool := &mockTxPool{}
	srv := NewServer("127.0.0.1:0", pool, 50000, 100000)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty listener address after Start")
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	txBytes := makeTxBytes(t)

	// Batch 1: send 5 transactions.
	accepted := writeBatch(t, conn, txBytes, 5)
	if accepted != 5 {
		t.Fatalf("batch 1: expected 5 accepted, got %d", accepted)
	}

	// Batch 2: send 3 more transactions on the same connection.
	accepted = writeBatch(t, conn, txBytes, 3)
	if accepted != 3 {
		t.Fatalf("batch 2: expected 3 accepted, got %d", accepted)
	}

	// Verify pool received all 8 transactions.
	if got := pool.Len(); got != 8 {
		t.Fatalf("pool: expected 8 txs, got %d", got)
	}

	// Verify cumulative server stats.
	injected, rejected, batches := srv.Stats()
	if injected != 8 {
		t.Fatalf("stats: expected 8 injected, got %d", injected)
	}
	if rejected != 0 {
		t.Fatalf("stats: expected 0 rejected, got %d", rejected)
	}
	if batches != 2 {
		t.Fatalf("stats: expected 2 batches, got %d", batches)
	}

	// Open a second connection and send another batch.
	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial conn2: %v", err)
	}
	defer conn2.Close()

	accepted = writeBatch(t, conn2, txBytes, 2)
	if accepted != 2 {
		t.Fatalf("conn2 batch: expected 2 accepted, got %d", accepted)
	}

	// Total should now be 10.
	if got := pool.Len(); got != 10 {
		t.Fatalf("pool after conn2: expected 10 txs, got %d", got)
	}

	injected, _, batches = srv.Stats()
	if injected != 10 {
		t.Fatalf("final stats: expected 10 injected, got %d", injected)
	}
	if batches != 3 {
		t.Fatalf("final stats: expected 3 batches, got %d", batches)
	}
}
