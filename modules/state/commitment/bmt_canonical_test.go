// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression: BMTRootComputer must be canonical — a per-block incremental root
// must equal a from-scratch rebuild over the same final live key set. This guards
// the fix that routed the computer through the sequential (canonical)
// UpdateAccount/UpdateStorage path instead of bmt.PutBatch, whose single-child
// node handling was non-canonical (build-from-empty collapses, update-internal
// keeps a single-child internal), causing the replay-v2 BMT root to diverge from
// a rebuild.

package commitment

import (
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/bmt"
)

func bmtAddr(i uint64) types.Address {
	var a types.Address
	binary.BigEndian.PutUint64(a[12:], i)
	binary.BigEndian.PutUint64(a[0:], i*0x9E3779B97F4A7C15)
	return a
}
func bmtAcct(n uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, Nonce: n}
	a.Balance.SetUint64(n*13 + 1)
	return a
}
func freshBMTComputer() *BMTRootComputer {
	return NewBMTRootComputer(NewBMTCommitment(bmt.New(bmt.NewMemStore())))
}
func slotHashB(b byte) types.Hash { var h types.Hash; h[31] = b; return h }

// TestBMTComputerIncrementalEqualsBatch hammers the canonical invariant with
// pseudo-random insert/delete/overwrite churn over accounts AND storage, then
// rebuilds the live set in one shot and compares.
func TestBMTComputerIncrementalEqualsBatch(t *testing.T) {
	inc := freshBMTComputer()
	liveAcct := map[uint64]uint64{}                 // addr -> nonce
	liveStor := map[uint64]map[byte]uint64{}        // addr -> slot -> val
	rng := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	// Storage lives on a DISJOINT address space (offset by 1<<40) from the
	// account-churn space, so account deletes never need to wipe storage (which
	// the IBS, not the computer, is responsible for) — this keeps the test a
	// clean check of computer canonicality.
	const storBase = uint64(1) << 40
	for round := 0; round < 80; round++ {
		accts := map[types.Address]*account.StateAccount{}
		stor := map[types.Address]map[types.Hash]*uint256.Int{}
		for op := 0; op < 10; op++ {
			k := next() % 300
			if next()%4 == 3 { // delete account
				accts[bmtAddr(k)] = nil
				delete(liveAcct, k)
			} else { // set account
				nn := next()%5000 + 1
				accts[bmtAddr(k)] = bmtAcct(nn)
				liveAcct[k] = nn
			}
			// a storage op on a disjoint address
			sk := storBase + next()%300
			sl := byte(next() % 16)
			if next()%4 == 3 { // delete slot
				if stor[bmtAddr(sk)] == nil {
					stor[bmtAddr(sk)] = map[types.Hash]*uint256.Int{}
				}
				stor[bmtAddr(sk)][slotHashB(sl)] = uint256.NewInt(0)
				if liveStor[sk] != nil {
					delete(liveStor[sk], sl)
				}
			} else {
				sv := next()%9999 + 1
				if stor[bmtAddr(sk)] == nil {
					stor[bmtAddr(sk)] = map[types.Hash]*uint256.Int{}
				}
				stor[bmtAddr(sk)][slotHashB(sl)] = uint256.NewInt(sv)
				if liveStor[sk] == nil {
					liveStor[sk] = map[byte]uint64{}
				}
				liveStor[sk][sl] = sv
			}
		}
		if _, err := inc.ComputeRoot(accts, stor); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	incRoot, _ := inc.ComputeRoot(nil, nil)

	// Rebuild from the final live set in one shot.
	batch := freshBMTComputer()
	bAccts := map[types.Address]*account.StateAccount{}
	for k, n := range liveAcct {
		bAccts[bmtAddr(k)] = bmtAcct(n)
	}
	bStor := map[types.Address]map[types.Hash]*uint256.Int{}
	for k, slots := range liveStor {
		if len(slots) == 0 {
			continue
		}
		m := map[types.Hash]*uint256.Int{}
		for sl, v := range slots {
			m[slotHashB(sl)] = uint256.NewInt(v)
		}
		bStor[bmtAddr(k)] = m
	}
	batchRoot, err := batch.ComputeRoot(bAccts, bStor)
	if err != nil {
		t.Fatal(err)
	}

	if incRoot != batchRoot {
		t.Fatalf("BMT computer incremental != batch over identical live set:\n  inc=%x\n  batch=%x\n  liveAccts=%d", incRoot, batchRoot, len(liveAcct))
	}
}
