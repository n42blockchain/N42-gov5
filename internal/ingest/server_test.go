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
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// mockTxPool implements TxPool for testing.
type mockTxPool struct {
	mu   sync.Mutex
	txs  []*transaction.Transaction
	fail bool // if true, AddLocal returns an error
}

func (p *mockTxPool) AddLocal(tx *transaction.Transaction) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return io.ErrClosedPipe
	}
	p.txs = append(p.txs, tx)
	return nil
}

func (p *mockTxPool) Stats() (pending, pendingAddrs, queued, queuedAddrs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.txs), 0, 0, 0
}

func (p *mockTxPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.txs)
}

// makeTxBytes creates a minimal serialized transaction for testing.
// It marshals a zero-value transaction.
func makeTxBytes(t *testing.T) []byte {
	t.Helper()
	tx := transaction.NewTx(&transaction.LegacyTx{})
	b, err := tx.Marshal()
	if err != nil {
		t.Fatalf("marshal tx: %v", err)
	}
	return b
}

// writeBatch writes a batch of num transactions to the connection.
func writeBatch(t *testing.T, conn net.Conn, txBytes []byte, num int) uint32 {
	t.Helper()

	// Write num_txs (4 bytes LE).
	var numBuf [4]byte
	binary.LittleEndian.PutUint32(numBuf[:], uint32(num))
	if _, err := conn.Write(numBuf[:]); err != nil {
		t.Fatalf("write num_txs: %v", err)
	}

	sender := types.Address{0x01, 0x02, 0x03}

	for i := 0; i < num; i++ {
		// tx_len (2 bytes LE).
		var lenBuf [2]byte
		binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(txBytes)))
		if _, err := conn.Write(lenBuf[:]); err != nil {
			t.Fatalf("write tx_len: %v", err)
		}

		// tx bytes.
		if _, err := conn.Write(txBytes); err != nil {
			t.Fatalf("write tx: %v", err)
		}

		// 20-byte sender.
		if _, err := conn.Write(sender[:]); err != nil {
			t.Fatalf("write sender: %v", err)
		}
	}

	// Read 4-byte LE accepted count.
	var resp [4]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return binary.LittleEndian.Uint32(resp[:])
}

func TestIngestServer_StartStop(t *testing.T) {
	pool := &mockTxPool{}
	srv := NewServer("127.0.0.1:0", pool, 50000, 100000)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty listener address")
	}

	srv.Stop()

	// After stop, Addr should return empty.
	if got := srv.Addr(); got != "" {
		t.Fatalf("expected empty addr after stop, got %q", got)
	}
}

func TestIngestServer_InjectBatch(t *testing.T) {
	pool := &mockTxPool{}
	srv := NewServer("127.0.0.1:0", pool, 50000, 100000)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	txBytes := makeTxBytes(t)
	const batchSize = 10

	accepted := writeBatch(t, conn, txBytes, batchSize)
	if accepted != batchSize {
		t.Fatalf("expected %d accepted, got %d", batchSize, accepted)
	}

	if pool.Len() != batchSize {
		t.Fatalf("expected pool to have %d txs, got %d", batchSize, pool.Len())
	}

	injected, rejected, batches := srv.Stats()
	if injected != batchSize {
		t.Fatalf("expected %d injected, got %d", batchSize, injected)
	}
	if rejected != 0 {
		t.Fatalf("expected 0 rejected, got %d", rejected)
	}
	if batches != 1 {
		t.Fatalf("expected 1 batch, got %d", batches)
	}
}

func TestIngestServer_Backpressure(t *testing.T) {
	pool := &mockTxPool{}
	// Set hardCap to 5 so that after the first batch of 10, subsequent
	// batches are rejected.
	srv := NewServer("127.0.0.1:0", pool, 3, 5)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	txBytes := makeTxBytes(t)

	// First batch: inject 10 txs (pool is empty, below hardCap).
	accepted := writeBatch(t, conn, txBytes, 10)
	if accepted != 10 {
		t.Fatalf("first batch: expected 10 accepted, got %d", accepted)
	}

	// Pool now has 10 txs, which exceeds hardCap (5).
	// Second batch should be rejected entirely.
	accepted = writeBatch(t, conn, txBytes, 5)
	if accepted != 0 {
		t.Fatalf("second batch: expected 0 accepted (backpressure), got %d", accepted)
	}

	_, rejected, _ := srv.Stats()
	if rejected != 5 {
		t.Fatalf("expected 5 rejected, got %d", rejected)
	}
}
