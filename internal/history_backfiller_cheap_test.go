// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"encoding/binary"
	"math/rand"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// seedWorkload writes changesets for blocks from..to: every one of hot
// accounts and hotSlots storage slots changes in every block, and each of
// sparse accounts changes with probability 1/2, which grows array-container
// histories past one chunk.
func seedWorkload(tb testing.TB, db kv.RwDB, from, to uint64, hot, hotSlots, sparse int, r *rand.Rand) {
	tb.Helper()
	for n := from; n <= to; n++ {
		require.NoError(tb, db.Update(context.Background(), func(tx kv.RwTx) error {
			w := state.NewPlainStateWriter(tx, tx, n)
			upd := func(id int) error {
				var addr types.Address
				binary.BigEndian.PutUint32(addr[16:], uint32(id))
				addr[0] = 0xa
				before := account.NewAccount()
				after := account.NewAccount()
				after.Nonce = n
				after.Balance = *uint256.NewInt(n)
				return w.UpdateAccountData(addr, &before, &after)
			}
			for i := 0; i < hot; i++ {
				if err := upd(i); err != nil {
					return err
				}
			}
			for i := 0; i < sparse; i++ {
				if r.Intn(2) == 0 {
					if err := upd(1_000_000 + i); err != nil {
						return err
					}
				}
			}
			var addr types.Address
			addr[0] = 0xc
			for i := 0; i < hotSlots; i++ {
				var slot types.Hash
				binary.BigEndian.PutUint32(slot[28:], uint32(i))
				if err := w.WriteAccountStorage(addr, slot, *uint256.NewInt(n - 1), *uint256.NewInt(n)); err != nil {
					return err
				}
			}
			return w.WriteChangeSets()
		}))
	}
}

// TestBackfillCheapPrepareIdentical folds the same changesets with
// state.HistoryCheapPrepare on and off, over many folds so later ones read
// back single and multi-chunk histories, and requires identical tables.
func TestBackfillCheapPrepareIdentical(t *testing.T) {
	run := func(cheap bool) (map[string]string, map[string]string) {
		prev := state.HistoryCheapPrepare
		state.HistoryCheapPrepare = cheap
		defer func() { state.HistoryCheapPrepare = prev }()
		db := memdb.NewTestDB(t)
		r := rand.New(rand.NewSource(7))
		var head uint64
		b := NewHistoryBackfiller(db, func() uint64 { return head }, 37, time.Hour)
		for head < 3_000 {
			seedWorkload(t, db, head+1, head+150, 3, 2, 4, r)
			head += 150
			for {
				folded, err := b.stepFold()
				require.NoError(t, err)
				if !folded {
					break
				}
			}
		}
		n, ok := b.IndexedThrough()
		require.True(t, ok)
		require.Equal(t, head, n)
		return dumpTable(t, db, modules.AccountsHistory), dumpTable(t, db, modules.StorageHistory)
	}
	accOff, stoOff := run(false)
	accOn, stoOn := run(true)
	require.Equal(t, accOff, accOn)
	require.Equal(t, stoOff, stoOn)
	// The workload must have produced multi-chunk keys (chunk suffix other
	// than ^uint64(0)), or the multi-chunk read path went untested.
	multi := 0
	for k := range accOn {
		if binary.BigEndian.Uint64([]byte(k[len(k)-8:])) != ^uint64(0) {
			multi++
		}
	}
	require.Greater(t, multi, 0)
}

// BenchmarkBackfillStepFold times folds of 12 blocks (two seconds of the
// bench fleet) over 8k hot accounts and 2k hot storage slots, on top of an
// index already holding 120 blocks of them.
func BenchmarkBackfillStepFold(b *testing.B) {
	const hot, slots, perFold, warm = 8_000, 2_000, 12, 120
	db := memdb.NewTestDB(b)
	r := rand.New(rand.NewSource(1))
	folds := uint64(b.N)
	seedWorkload(b, db, 1, warm+folds*perFold, hot, slots, 0, r)
	head := uint64(warm)
	bf := NewHistoryBackfiller(db, func() uint64 { return head }, warm, time.Hour)
	_, err := bf.stepFold()
	require.NoError(b, err)
	bf.batch = perFold
	prevYield := historyFoldYield
	historyFoldYield = 0 // time the work, not the pauses
	defer func() { historyFoldYield = prevYield }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		head += perFold
		folded, err := bf.stepFold()
		if err != nil || !folded {
			b.Fatal(err, folded)
		}
	}
}
