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

package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bal"
)

// spyReader records which accounts and slots prewarmFromBAL touched.
type spyReader struct {
	accounts map[types.Address]int
	slots    map[[2]string]int
}

func newSpyReader() *spyReader {
	return &spyReader{accounts: map[types.Address]int{}, slots: map[[2]string]int{}}
}

func (s *spyReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	s.accounts[address]++
	return nil, nil
}

func (s *spyReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	s.slots[[2]string{string(address[:]), string(key[:])}] = s.slots[[2]string{string(address[:]), string(key[:])}] + 1
	return nil, nil
}

func addr(b byte) types.Address { var a types.Address; a[19] = b; return a }
func slot(b byte) types.Hash    { var h types.Hash; h[31] = b; return h }

func TestPrewarmFromBALWarmsTouchedState(t *testing.T) {
	// Build a BAL: addr01 writes slot02, reads slot07; addr02 writes slot09.
	txs := []bal.TxAccess{
		{TxIndex: 1,
			StorageWrites: []bal.SlotWrite{
				{Address: addr(0x01), Slot: slot(0x02), NewValue: slot(0x02)},
				{Address: addr(0x02), Slot: slot(0x09), NewValue: slot(0x09)},
			},
			StorageReads:   []bal.SlotRead{{Address: addr(0x01), Slot: slot(0x07)}},
			BalanceChanges: []bal.AccountBalance{{Address: addr(0x01), PostBalance: *uint256.NewInt(1)}},
		},
	}
	b := bal.BuildBAL(txs)

	spy := newSpyReader()
	PrewarmFromBAL(spy, b)

	// Both accounts warmed exactly once.
	if spy.accounts[addr(0x01)] != 1 || spy.accounts[addr(0x02)] != 1 {
		t.Fatalf("accounts warmed = %v, want addr01=1 addr02=1", spy.accounts)
	}
	if len(spy.accounts) != 2 {
		t.Fatalf("warmed %d accounts, want 2", len(spy.accounts))
	}
	// Slots: addr01/slot02 (write), addr01/slot07 (read), addr02/slot09 (write).
	check := func(a types.Address, s types.Hash) {
		if spy.slots[[2]string{string(a[:]), string(s[:])}] != 1 {
			t.Fatalf("slot (%x,%x) warmed %d times, want 1", a, s, spy.slots[[2]string{string(a[:]), string(s[:])}])
		}
	}
	check(addr(0x01), slot(0x02))
	check(addr(0x01), slot(0x07))
	check(addr(0x02), slot(0x09))
	if len(spy.slots) != 3 {
		t.Fatalf("warmed %d slots, want 3", len(spy.slots))
	}
}

func TestPrewarmFromBALNilSafe(t *testing.T) {
	PrewarmFromBAL(nil, nil)          // must not panic
	PrewarmFromBAL(newSpyReader(), nil) // must not panic
}

// buildRealisticBAL constructs a BAL the size of a busy block: nAcct accounts,
// each writing/reading slotsPer storage slots, plus balance changes.
func buildRealisticBAL(nAcct, slotsPer int) *bal.BlockAccessList {
	txs := make([]bal.TxAccess, 0, nAcct)
	for a := 0; a < nAcct; a++ {
		var ad types.Address
		ad[18], ad[19] = byte(a>>8), byte(a)
		ta := bal.TxAccess{
			TxIndex:        uint16(a + 1),
			BalanceChanges: []bal.AccountBalance{{Address: ad, PostBalance: *uint256.NewInt(uint64(a) + 1)}},
		}
		for s := 0; s < slotsPer; s++ {
			var sl, vv types.Hash
			sl[30], sl[31] = byte(s>>8), byte(s)
			vv[31] = byte(s + 1)
			ta.StorageWrites = append(ta.StorageWrites, bal.SlotWrite{Address: ad, Slot: sl, NewValue: vv})
		}
		txs = append(txs, ta)
	}
	return bal.BuildBAL(txs)
}

// BenchmarkPrewarmFromBAL measures the per-block cost of the prewarm dispatch (the
// controllable overhead of the QMDB-native prefetch mechanism) against a no-op
// reader — isolating the mechanism cost from backing-store read latency. The
// actual benefit (overlapping QMDB cold reads with execution) scales with the
// backing store's read latency and must be measured on a replay run; on a fast
// (cached/mem) reader the delta is small by design.
func BenchmarkPrewarmFromBAL(b *testing.B) {
	blk := buildRealisticBAL(200, 8) // ~200 accts, ~1600 slots — a busy block
	spy := newSpyReader()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PrewarmFromBAL(spy, blk)
	}
}
