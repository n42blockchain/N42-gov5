package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

// TestG64AddTxsOnNewBlock_RejectedTxnIsDiscardedImmediately covers
// addTxsOnNewBlock's own discard path (lifecycle.go:237-239): unlike addTxs,
// it calls p.discardLocked itself for any newTxs entry addLocked refuses,
// since there is no outer caller collecting per-txn discard reasons here. A
// same-sender/same-nonce unwound txn with too small a price bump must hit
// addLocked's NotReplaced branch and then be discarded on the spot, leaving
// the original txn as the sole occupant of that nonce slot.
func TestG64AddTxsOnNewBlock_RejectedTxnIsDiscardedImmediately(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	original := &types.TxSlot{
		Tip: *uint256.NewInt(100000), FeeCap: *uint256.NewInt(100000), Gas: 100000, Nonce: 0,
	}
	original.IDHash[0] = 0x61
	original.Rlp = []byte{0x61}
	var slots types.TxSlots
	slots.Append(original, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Equal(t, txpoolcfg.Success, reasons[0])

	// Same sender, same nonce, same tip/feeCap: no price bump at all, so
	// addLocked's replace check returns NotReplaced and addTxsOnNewBlock
	// must discard it immediately instead of leaving it half-added.
	weak := &types.TxSlot{
		Tip: *uint256.NewInt(100000), FeeCap: *uint256.NewInt(100000), Gas: 100000, Nonce: 0,
	}
	weak.IDHash[0] = 0x62
	weak.Rlp = []byte{0x62}
	var unwind types.TxSlots
	unwind.Append(weak, addr[:], true)

	h1 := gointerfaces.ConvertHashToH256([32]byte{5})
	change := &remote.StateChangeBatch{
		StateVersionId:      1,
		PendingBlockBaseFee: 50000,
		BlockGasLimit:       1000000,
		ChangeBatch:         []*remote.StateChange{{BlockHeight: 1, BlockHash: h1}},
	}
	require.NoError(t, pool.OnNewBlock(ctx, change, unwind, types.TxSlots{}, types.TxSlots{}, tx))

	require.Nil(t, pool.byHash[string(weak.IDHash[:])], "the weak unwound txn must have been discarded, not tracked")
	require.NotNil(t, pool.byHash[string(original.IDHash[:])], "the original txn must still be the one occupying nonce 0")

	pending, baseFee, queued := pool.CountContent()
	require.Equal(t, 1, pending+baseFee+queued, "only the original txn should remain in the pool")
}
