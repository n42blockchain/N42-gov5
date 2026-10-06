// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// TestQMDBComputerBatchedApplyMatchesEager: ComputeRoot applies a block's
// ops under the tree's leaf batch (SIMD leaf hashes, one path fold per
// touched twig level, one bitmap hash per twig). Its roots must be
// byte-identical to a tree fed the same sorted ops eagerly, across blocks
// that span several twigs and mix inserts, overwrites, storage writes and
// deletes.
func TestQMDBComputerBatchedApplyMatchesEager(t *testing.T) {
	rc := NewQMDBRootComputer()
	eager := qmdb.New()

	rng := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	addrOf := func(k uint64) types.Address {
		var a types.Address
		a[12], a[13], a[14], a[15] = byte(k>>24), byte(k>>16), byte(k>>8), byte(k)
		return a
	}
	slotOf := func(k uint64) types.Hash {
		var h types.Hash
		h[30], h[31] = byte(k>>8), byte(k)
		return h
	}

	type eop struct {
		kh  qmdb.Hash
		val []byte
	}
	for blk := 0; blk < 8; blk++ {
		accts := map[types.Address]*account.StateAccount{}
		stor := map[types.Address]map[types.Hash]*uint256.Int{}
		// Enough distinct keys per block to spread across many twigs (2048
		// leaves each), with overwrites of earlier blocks' keys.
		for i := 0; i < 6000; i++ {
			k := next() % 20000
			addr := addrOf(k)
			switch next() % 9 {
			case 0:
				accts[addr] = nil // delete (or a no-op for a key never set)
			case 1, 2:
				s := stor[addr]
				if s == nil {
					s = map[types.Hash]*uint256.Int{}
					stor[addr] = s
				}
				v := uint256.NewInt(next())
				if next()%5 == 0 {
					v = uint256.NewInt(0) // slot delete
				}
				s[slotOf(next()%64)] = v
				if _, ok := accts[addr]; !ok {
					accts[addr] = &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(k + 1)}
				}
			default:
				accts[addr] = &account.StateAccount{Initialised: true, Nonce: uint64(blk + 1), Balance: *uint256.NewInt(next() + 1)}
			}
		}
		got, err := rc.ComputeRoot(accts, stor)
		if err != nil {
			t.Fatalf("block %d: %v", blk, err)
		}

		// The same ops, sorted the same way, applied one eager Set/Delete
		// at a time.
		var ops []eop
		for addr, a := range accts {
			kh := qmdb.Hash(AccountKeyHash(addr))
			if a == nil || isAccountEmpty(a) {
				ops = append(ops, eop{kh: kh})
			} else {
				ops = append(ops, eop{kh: kh, val: EncodeAccountValue(a)})
			}
		}
		for addr, slots := range stor {
			for slot, v := range slots {
				kh := qmdb.Hash(StorageKeyHash(addr, slot))
				if v == nil || v.IsZero() {
					ops = append(ops, eop{kh: kh})
				} else {
					var buf [32]byte
					v.WriteToSlice(buf[:])
					ops = append(ops, eop{kh: kh, val: append([]byte(nil), buf[:]...)})
				}
			}
		}
		sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].kh[:], ops[j].kh[:]) < 0 })
		for _, o := range ops {
			if o.val == nil {
				eager.Delete(o.kh)
			} else {
				eager.Set(o.kh, o.val)
			}
		}
		if want := types.Hash(eager.Root()); got != want {
			t.Fatalf("block %d (%d ops): batched root %x != eager %x", blk, len(ops), got[:8], want[:8])
		}
		if rc.Tree().LiveCount() != eager.LiveCount() {
			t.Fatalf("block %d: live count %d != eager %d", blk, rc.Tree().LiveCount(), eager.LiveCount())
		}
	}
}
