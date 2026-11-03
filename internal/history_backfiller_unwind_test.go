// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
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
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// switchBlock unwinds block n and writes the changeset of a sibling that
// touches a different account, as a branch switch does.
func switchBlock(t *testing.T, db kv.RwDB, n uint64, sibling types.Address) {
	t.Helper()
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := commitment.UnwindPlainStateBlock(tx, n); err != nil {
			return err
		}
		w := state.NewPlainStateWriter(tx, tx, n)
		before, after := account.NewAccount(), account.NewAccount()
		after.Nonce = n
		after.Balance = *uint256.NewInt(n)
		if err := w.UpdateAccountData(sibling, &before, &after); err != nil {
			return err
		}
		return w.WriteChangeSets()
	}))
}

func indexed(t *testing.T, db kv.RwDB, addr types.Address) bool {
	t.Helper()
	found := false
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.AccountsHistory)
		if err != nil {
			return err
		}
		defer c.Close()
		k, _, err := c.Seek(addr[:])
		found = k != nil && len(k) >= len(addr) && types.BytesToAddress(k[:len(addr)]) == addr
		return err
	}))
	return found
}

// A block replaced after it was folded leaves the marker above changesets the
// index never saw, and the gate then trusts a tail that was not indexed.
func TestBackfillRefoldsABlockReplacedByABranchSwitch(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 6)
	b := NewHistoryBackfiller(db, func() uint64 { return 6 }, 16, time.Hour)
	defer b.Stop()
	require.NoError(t, b.step())

	sibling := types.Address{0: 0xAA}
	switchBlock(t, db, 6, sibling)

	require.NoError(t, b.step())
	marker, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(6), marker)
	require.True(t, indexed(t, db, sibling), "the sibling's change must reach the index")
}

// A fold prepared before an unwind describes changesets that no longer exist
// and must not claim the replacement block as covered.
func TestBackfillDropsAFoldPreparedBeforeAnUnwind(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 6)
	b := NewHistoryBackfiller(db, func() uint64 { return 6 }, 3, time.Hour)
	defer b.Stop()
	require.NoError(t, b.step()) // marker 3; the next fold covers 4..6

	sibling := types.Address{0: 0xAA}
	b.beforeApply = func() {
		b.beforeApply = nil
		switchBlock(t, db, 6, sibling)
	}
	require.NoError(t, b.step())
	require.NoError(t, b.step())

	marker, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(6), marker)
	require.True(t, indexed(t, db, sibling), "the sibling's change must reach the index")
}

// Re-executing a height clears the earlier attempt's changesets without an
// unwind, and the replacement has to be folded in the same way.
func TestBackfillRefoldsABlockReExecutedInPlace(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 6)
	b := NewHistoryBackfiller(db, func() uint64 { return 6 }, 16, time.Hour)
	defer b.Stop()
	require.NoError(t, b.step())

	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		return truncateStaleChangesets(tx, 6)
	}))

	marker, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(5), marker)
}
