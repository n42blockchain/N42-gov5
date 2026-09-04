// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func countRows(t *testing.T, tx kv.Tx, table string) int {
	t.Helper()
	c, err := tx.Cursor(table)
	require.NoError(t, err)
	defer c.Close()
	n := 0
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		require.NoError(t, err)
		n++
	}
	return n
}

// N42_NO_HISTORY_INDEX must drop ONLY the inverted index. The changesets it
// leaves behind are load-bearing three ways -- PlainState's only rewind source
// on the native chain, eth-el's separate path, and the DATC archive's only
// input -- and a bug that skipped both would break rewind and make historical
// proofs unbuildable, silently and months later.
//
// The flag itself latches on first read (correct for a node, untestable here),
// so this asserts the property the flag relies on: WriteChangeSets and
// WriteHistory are independent, and running the first without the second
// leaves the changesets intact and the index empty.
func TestChangeSetsSurviveWithoutTheHistoryIndex(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xAA

	w := NewPlainStateWriter(tx, tx, 1)
	before := account.NewAccount()
	after := account.NewAccount()
	after.Balance = *uint256.NewInt(42)
	after.Nonce = 1
	require.NoError(t, w.UpdateAccountData(addr, &before, &after))

	require.NoError(t, w.WriteChangeSets(), "changesets must be written")
	// Deliberately NOT calling WriteHistory: this is what the flag does.

	require.Positive(t, countRows(t, tx, modules.AccountChangeSet),
		"the changeset rows must exist — rewind and the DATC archive have no other source")
	require.Zero(t, countRows(t, tx, modules.AccountsHistory),
		"the inverted index must be empty when WriteHistory is skipped")
}

// The converse, so the test above cannot pass for the wrong reason (a writer
// that records nothing at all would also leave the index empty).
func TestHistoryIndexIsWrittenWhenNotSkipped(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xBB

	w := NewPlainStateWriter(tx, tx, 1)
	before := account.NewAccount()
	after := account.NewAccount()
	after.Balance = *uint256.NewInt(7)
	after.Nonce = 1
	require.NoError(t, w.UpdateAccountData(addr, &before, &after))
	require.NoError(t, w.WriteChangeSets())
	require.NoError(t, w.csw.WriteHistory(), "the unflagged path must write the index")

	require.Positive(t, countRows(t, tx, modules.AccountsHistory),
		"without the flag the index must be populated, or the first test proves nothing")
}
