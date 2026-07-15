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

package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func balAddr(b byte) types.Address { var a types.Address; a[19] = b; return a }
func balSlot(b byte) types.Hash    { var h types.Hash; h[31] = b; return h }

// feedTx simulates one transaction's FinalizeTx writes into a capture.
func feedTx(c *BALCapture, hash types.Hash, addr types.Address, slot types.Hash, val uint64, nonce uint64) {
	c.BeginTx(hash)
	_ = c.WriteAccountStorage(addr, slot, uint256.Int{}, *uint256.NewInt(val))
	acc := &account.StateAccount{Nonce: nonce, Balance: *uint256.NewInt(val)}
	_ = c.UpdateAccountData(addr, nil, acc)
}

// TestBALCaptureMinerImporterAgree is the activation-critical invariant: the miner
// (which may capture reverted/dropped txs during building) and the importer (which
// executes only the final txs) compute an identical block access list hash,
// because HashFor filters and re-indexes by the final block-ordered tx hashes.
func TestBALCaptureMinerImporterAgree(t *testing.T) {
	hA := balSlot(0xa1)
	hB := balSlot(0xb2)
	hReverted := balSlot(0xcc)

	// Miner side: tries A, a reverted tx, then B (reverted one is not in the block).
	miner := NewBALCapture(state.NewNoopWriter())
	feedTx(miner, hA, balAddr(0x01), balSlot(0x02), 100, 1)
	feedTx(miner, hReverted, balAddr(0x09), balSlot(0x09), 999, 9) // dropped from block
	feedTx(miner, hB, balAddr(0x02), balSlot(0x05), 200, 1)

	// Importer side: executes only the final block txs A, B in order.
	imp := NewBALCapture(state.NewNoopWriter())
	feedTx(imp, hA, balAddr(0x01), balSlot(0x02), 100, 1)
	feedTx(imp, hB, balAddr(0x02), balSlot(0x05), 200, 1)

	order := []types.Hash{hA, hB} // the block's finally-included txs

	minerHash, err := miner.HashFor(order)
	if err != nil {
		t.Fatal(err)
	}
	impHash, err := imp.HashFor(order)
	if err != nil {
		t.Fatal(err)
	}
	if minerHash != impHash {
		t.Fatalf("miner and importer BAL hash differ: %s vs %s", minerHash.Hex(), impHash.Hex())
	}
	if (minerHash == types.Hash{}) {
		t.Fatal("non-empty block hashed BAL to zero")
	}
}

// TestBALCaptureFinalizeTxDeterministic is the integration proof: two independent
// but identical executions drive IntraBlockState.FinalizeTx into a BALCapture and
// must yield the same block-access-list hash. This exercises the real finalize ->
// capture path the miner and importer both rely on (they execute the same block,
// so they feed FinalizeTx the same writes and compute the same hash) — the
// cross-node determinism reduces to the state-execution determinism the chain
// already guarantees for the state root.
func TestBALCaptureFinalizeTxDeterministic(t *testing.T) {
	sender := types.HexToAddress("0x1000000000000000000000000000000000000001")
	recip := types.HexToAddress("0x1000000000000000000000000000000000000002")
	hA := balSlot(0xa1)
	hB := balSlot(0xb2)

	run := func() types.Hash {
		db := memdb.NewTestDB(t)
		tx := memdb.BeginRw(t, db)
		ibs := state.New(state.NewPlainState(tx, 1))
		ibs.CreateAccount(sender, false)
		ibs.AddBalance(sender, uint256.NewInt(1_000_000))
		rules := &params.Rules{IsSpuriousDragon: true}
		cap := NewBALCapture(state.NewNoopWriter())

		// tx A: transfer 100 sender -> recip
		ibs.Prepare(hA, types.Hash{}, 0)
		cap.BeginTx(hA)
		ibs.SubBalance(sender, uint256.NewInt(100))
		ibs.AddBalance(recip, uint256.NewInt(100))
		if err := ibs.FinalizeTx(rules, cap); err != nil {
			t.Fatal(err)
		}
		// tx B: transfer 50 sender -> recip
		ibs.Prepare(hB, types.Hash{}, 1)
		cap.BeginTx(hB)
		ibs.SubBalance(sender, uint256.NewInt(50))
		ibs.AddBalance(recip, uint256.NewInt(50))
		if err := ibs.FinalizeTx(rules, cap); err != nil {
			t.Fatal(err)
		}

		h, err := cap.HashFor([]types.Hash{hA, hB})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	h1 := run()
	h2 := run()
	if h1 != h2 {
		t.Fatalf("BAL hash not deterministic across identical executions: %s vs %s", h1.Hex(), h2.Hex())
	}
	if (h1 == types.Hash{}) {
		t.Fatal("non-empty execution produced a zero BAL hash — FinalizeTx writes not captured")
	}
}

// TestBALCaptureExcludesDroppedTx checks a captured tx absent from the final order
// does not affect the hash (its writes are excluded).
func TestBALCaptureExcludesDroppedTx(t *testing.T) {
	hA := balSlot(0xa1)
	hDropped := balSlot(0xdd)

	withDropped := NewBALCapture(state.NewNoopWriter())
	feedTx(withDropped, hA, balAddr(0x01), balSlot(0x02), 100, 1)
	feedTx(withDropped, hDropped, balAddr(0x07), balSlot(0x07), 7, 7)

	clean := NewBALCapture(state.NewNoopWriter())
	feedTx(clean, hA, balAddr(0x01), balSlot(0x02), 100, 1)

	order := []types.Hash{hA}
	a, _ := withDropped.HashFor(order)
	b, _ := clean.HashFor(order)
	if a != b {
		t.Fatalf("dropped tx leaked into BAL: %s vs %s", a.Hex(), b.Hex())
	}
}

// TestBALCaptureOrderMatters checks tx ordering changes the hash (tx index binds).
func TestBALCaptureOrderMatters(t *testing.T) {
	hA := balSlot(0xa1)
	hB := balSlot(0xb2)
	c := NewBALCapture(state.NewNoopWriter())
	feedTx(c, hA, balAddr(0x01), balSlot(0x02), 100, 1)
	feedTx(c, hB, balAddr(0x02), balSlot(0x05), 200, 1)

	ab, _ := c.HashFor([]types.Hash{hA, hB})
	ba, _ := c.HashFor([]types.Hash{hB, hA})
	if ab == ba {
		t.Fatal("BAL hash ignored tx order")
	}
}
