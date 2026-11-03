package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// Historical reads need the FIRST change AFTER the requested height. An
// indexed prefix alone does not cover keys that change only in the missing tail.
func TestDeferredHistoryRejectsMissingTail(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	addr := types.Address{19: 0xAA}
	before, after := account.NewAccount(), account.NewAccount()
	before.Initialised, after.Initialised = true, true
	before.Nonce, after.Nonce = 1, 2
	w := state.NewPlainStateWriter(tx, tx, 900)
	require.NoError(t, w.UpdateAccountData(addr, &before, &after))
	require.NoError(t, w.WriteChangeSets())
	require.NoError(t, rawdb.WriteHistoryIndexedThrough(tx, 500))

	// Without a gate, a query far BELOW the marker incorrectly sees nonce 2.
	got, err := state.NewPlainState(tx, 101).ReadAccountData(addr)
	require.NoError(t, err)
	require.Equal(t, uint64(2), got.Nonce)
	for _, requested := range []uint64{100, 500, 900} {
		require.True(t, deferredRefusesQuery(requested, 1000, 500, true),
			"must refuse historical block %d until the tail is indexed", requested)
	}
	require.False(t, deferredRefusesQuery(1000, 1000, 500, true), "latest stays available")

	// Once the tail is indexed, the same historical reader recovers nonce 1.
	agg := state.NewHistoryAggregator()
	agg.AddKey(modules.AccountsHistory, addr[:], 900)
	require.NoError(t, agg.Flush(tx))
	require.NoError(t, rawdb.WriteHistoryIndexedThrough(tx, 1000))
	got, err = state.NewPlainState(tx, 101).ReadAccountData(addr)
	require.NoError(t, err)
	require.Equal(t, uint64(1), got.Nonce)
	require.False(t, deferredRefusesQuery(100, 1000, 1000, true))
}
