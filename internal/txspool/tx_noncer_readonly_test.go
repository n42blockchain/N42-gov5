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

package txspool

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestTxNoncerGetReadOnlyDoesNotCacheUnknown pins the reth #23008 hardening:
// the read-only nonce path (TxsPool.Nonce → getReadOnly, reachable from
// eth_getTransactionCount "pending" with arbitrary addresses) must NOT grow the
// pending-nonce map for addresses the pool does not track, otherwise a flood of
// RPC queries for random addresses balloons the map until the next pool reset.
func TestTxNoncerGetReadOnlyDoesNotCacheUnknown(t *testing.T) {
	mock := newMockReadState()
	n := newTxNoncer(mock)

	// A genuinely tracked sender (cached via the insertion path).
	tracked := types.Address{0x01}
	n.set(tracked, 9)

	// A flood of unknown addresses via the read-only path: each returns the
	// state nonce (0) and must NOT be cached.
	for i := 0; i < 1000; i++ {
		var a types.Address
		a[0] = 0xAA
		binary.BigEndian.PutUint64(a[12:], uint64(i))
		if got := n.getReadOnly(a); got != 0 {
			t.Fatalf("unknown addr %d: got %d want 0", i, got)
		}
	}
	if len(n.nonces) != 1 {
		t.Fatalf("getReadOnly grew the nonce cache to %d entries (expected only the 1 tracked sender)", len(n.nonces))
	}

	// A tracked sender still returns its cached pending nonce.
	if got := n.getReadOnly(tracked); got != 9 {
		t.Fatalf("tracked sender: got %d want 9", got)
	}

	// Control: get() (the insertion path) DOES cache an unknown address — the
	// exact behaviour getReadOnly must avoid on the RPC path.
	var u types.Address
	u[0] = 0xBB
	_ = n.get(u)
	if _, ok := n.nonces[u]; !ok {
		t.Fatal("control: get() should have cached the unknown address")
	}
}
