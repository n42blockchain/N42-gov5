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

package parallel

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// transferBase is a base state of senders and recipients.
type transferBase struct {
	accs map[types.Address]*account.StateAccount
}

func (b *transferBase) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	acc := b.accs[a]
	if acc == nil {
		return nil, nil
	}
	c := *acc
	return &c, nil
}
func (b *transferBase) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (b *transferBase) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (b *transferBase) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

func transferAddr(kind byte, i int) types.Address {
	var a types.Address
	a[0] = kind
	binary.BigEndian.PutUint32(a[1:5], uint32(i)*2654435761)
	binary.BigEndian.PutUint32(a[16:20], uint32(i))
	return a
}

type transferWorker struct {
	rd *ParallelStateReader
	wr *ParallelStateWriter
}

// runTransferBlock runs the bench's block shape on the production reader
// and writer (entry cache, base cache, account buffer, slabs, affinity by
// sender): S senders with chains of L transfers of 1 wei to R recipients.
// It returns the senders' nonces and the recipients' credits after the
// apply walk.
func runTransferBlock(t *testing.T, base *transferBase, S, L, R, workers int, recipient func(i int) int) (map[types.Address]uint64, map[types.Address]uint64, *Executor) {
	numTxs := S * L
	sender := func(i int) int { return i % S }
	var ex *Executor
	baseCache := NewBaseCache(S + R)
	setup := func(int) (any, func(), error) {
		w := &transferWorker{rd: NewParallelStateReader(base, ex.MVS(), nil, 0), wr: NewParallelStateWriter(nil)}
		w.rd.SetBaseCache(baseCache)
		w.rd.EnableEntryCache()
		w.rd.ReuseAccountBuffer()
		slab := NewValueSlab()
		w.rd.SetSlab(slab)
		w.wr.SetSlab(slab)
		return w, nil, nil
	}
	ex = NewExecutorWithWorkerSetup(numTxs, workers, setup, func(ctx any, i int, rw *ReadWriteSet) error {
		w := ctx.(*transferWorker)
		w.rd.Rebind(rw, i)
		w.wr.Rebind(rw)
		from, to := transferAddr(1, sender(i)), transferAddr(2, recipient(i))
		acc, err := w.rd.ReadAccountData(from)
		if err != nil || acc == nil {
			return fmt.Errorf("sender %d: %v", i, err)
		}
		fromOrig := *acc
		fromNew := fromOrig
		acc, err = w.rd.ReadAccountIgnoringBalance(to)
		if err != nil || acc == nil {
			return fmt.Errorf("recipient %d: %v", i, err)
		}
		toOrig := *acc
		toNew := toOrig
		fromNew.Nonce++
		fromNew.Balance.Sub(&fromNew.Balance, uint256.NewInt(1))
		toNew.Balance.Add(&toNew.Balance, uint256.NewInt(1))
		w.wr.UpdateAccountDataEligible(from, &fromOrig, &fromNew, false)
		w.wr.UpdateAccountDataEligible(to, &toOrig, &toNew, true)
		return nil
	})
	ex.SetAffinity(func(i int) uint64 {
		a := transferAddr(1, sender(i))
		return binary.LittleEndian.Uint64(a[:8])
	})
	ex.PrepareAffinity()
	for i, r := range ex.Run() {
		if r.Err != nil {
			t.Fatalf("tx %d: %v", i, r.Err)
		}
	}
	nonces := map[types.Address]uint64{}
	credits := map[types.Address]uint64{}
	err := ex.MVS().ApplyAll(numTxs, func(key LocationKey, value []byte, delta *uint256.Int) error {
		if key.Field != FieldBalance {
			return nil
		}
		if value != nil {
			acc, err := DecodeAccount(value)
			if err != nil {
				return err
			}
			if key.Address[0] == 1 {
				if _, dup := nonces[key.Address]; dup {
					return fmt.Errorf("sender %x applied twice", key.Address)
				}
				nonces[key.Address] = acc.Nonce
				return nil
			}
			return fmt.Errorf("recipient %x has a full write", key.Address)
		}
		if _, dup := credits[key.Address]; dup {
			return fmt.Errorf("recipient %x applied twice", key.Address)
		}
		credits[key.Address] = delta.Uint64()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return nonces, credits, ex
}

// TestPresizedTransferBlocksExact runs transfer blocks of the bench's shape
// with the store presized, two executors at a time (a leader's import and
// its ahead run share the arena pools), and checks every block's apply walk
// against the serial result: every sender's nonce, every recipient's
// credits. N42_TEST_TRANSFER_ROUNDS raises the round count.
func TestPresizedTransferBlocksExact(t *testing.T) {
	if !presizeMVS {
		t.Skip("N42_MVS_PRESIZE=0")
	}
	rounds := 6
	if v, err := strconv.Atoi(os.Getenv("N42_TEST_TRANSFER_ROUNDS")); err == nil && v > 0 {
		rounds = v
	}
	S, L, R, W := 2000, 10, 600, 8
	if testing.Short() {
		S, L = 800, 10
	}
	if v := os.Getenv("N42_TEST_TRANSFER_SHAPE"); v != "" {
		if _, err := fmt.Sscanf(v, "%d,%d,%d,%d", &S, &L, &R, &W); err != nil {
			t.Fatalf("N42_TEST_TRANSFER_SHAPE %q: %v", v, err)
		}
	}
	base := &transferBase{accs: map[types.Address]*account.StateAccount{}}
	for s := 0; s < S; s++ {
		acc := &account.StateAccount{}
		acc.Balance.SetUint64(1 << 40)
		base.accs[transferAddr(1, s)] = acc
	}
	for r := 0; r < R; r++ {
		acc := &account.StateAccount{}
		acc.Balance.SetUint64(1000)
		base.accs[transferAddr(2, r)] = acc
	}
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		for lane := 0; lane < 2; lane++ {
			wg.Add(1)
			go func(lane int) {
				defer wg.Done()
				seed := round*7 + lane
				recipient := func(i int) int { return (i*31 + seed*977 + (i/S)*13) % R }
				nonces, credits, ex := runTransferBlock(t, base, S, L, R, W, recipient)
				defer ex.Release()
				for s := 0; s < S; s++ {
					if n := nonces[transferAddr(1, s)]; n != uint64(L) {
						t.Errorf("round %d lane %d: sender %d nonce %d, want %d", round, lane, s, n, L)
						return
					}
				}
				want := make([]uint64, R)
				for i := 0; i < S*L; i++ {
					want[recipient(i)]++
				}
				for r := 0; r < R; r++ {
					if got := credits[transferAddr(2, r)]; got != want[r] {
						t.Errorf("round %d lane %d: recipient %d credited %d, want %d", round, lane, r, got, want[r])
						return
					}
				}
			}(lane)
		}
		wg.Wait()
		if t.Failed() {
			return
		}
	}
}
