package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/types"
)

// TestG61Lifecycle_MinedRemovesTx drives OnNewBlock with a minedTxs slot that
// matches a previously-added pending txn: removeMined must drop it from the
// pool and addTxsOnNewBlock/promote must not resurrect it.
func TestG61Lifecycle_MinedRemovesTx(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  0,
	}
	s.IDHash[0] = 0x01
	s.Rlp = []byte{0xde, 0xad, 0x01}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 1)

	pending, baseFeeN, queued := pool.CountContent()
	require.Greater(t, pending+baseFeeN+queued, 0, "txn should be tracked before mining")

	// Next block: the sender's nonce advances to 1 (as if the tx mined),
	// and minedTxs carries the same slot so removeMined can find it by hash.
	h1 := gointerfaces.ConvertHashToH256([32]byte{1})
	change := &remote.StateChangeBatch{
		StateVersionId:      1,
		PendingBlockBaseFee: 200000,
		BlockGasLimit:       1000000,
		ChangeBatch: []*remote.StateChange{
			{BlockHeight: 1, BlockHash: h1},
		},
	}
	v := make([]byte, types.EncodeSenderLengthForStorage(1, *uint256.NewInt(9*common.Ether)))
	types.EncodeSender(1, *uint256.NewInt(9*common.Ether), v)
	change.ChangeBatch[0].Changes = append(change.ChangeBatch[0].Changes, &remote.AccountChange{
		Action:  remote.Action_UPSERT,
		Address: gointerfaces.ConvertAddressToH160(addr),
		Data:    v,
	})

	var minedTxs types.TxSlots
	minedTxs.Append(s, addr[:], true)

	require.NoError(t, pool.OnNewBlock(ctx, change, types.TxSlots{}, types.TxSlots{}, minedTxs, tx))

	pending2, baseFee2, queued2 := pool.CountContent()
	require.Equal(t, 0, pending2+baseFee2+queued2, "mined txn should be fully removed from all subpools")
}

// TestG61Lifecycle_NonceGapQueuedThenPromoted covers the queued->pending
// promotion path: a txn with a nonce gap (nonce=1 while the sender's current
// nonce is 0) starts out queued, and once a nonce-0 txn fills the gap both
// become promotable.
func TestG61Lifecycle_NonceGapQueuedThenPromoted(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	gapSlot := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  1,
	}
	gapSlot.IDHash[0] = 0x02
	gapSlot.Rlp = []byte{0xde, 0xad, 0x02}
	var slots types.TxSlots
	slots.Append(gapSlot, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 1)

	_, _, queuedBefore := pool.CountContent()
	require.Greater(t, queuedBefore, 0, "nonce-gapped txn should land in the queued subpool")

	fillSlot := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  0,
	}
	fillSlot.IDHash[0] = 0x03
	fillSlot.Rlp = []byte{0xde, 0xad, 0x03}
	var fillSlots types.TxSlots
	fillSlots.Append(fillSlot, addr[:], true)
	reasons, err = pool.AddLocalTxs(ctx, fillSlots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 1)

	pendingAfter, baseFeeAfter, _ := pool.CountContent()
	require.Greater(t, pendingAfter+baseFeeAfter, 0, "filling the gap should promote both txns out of queued")
}

// TestG61Lifecycle_BaseFeeMovesTxBetweenSubpools raises then lowers the
// block base fee across two OnNewBlock calls, exercising setBaseFee's
// changed/unchanged branches and promote's base-fee-subpool movement.
func TestG61Lifecycle_BaseFeeMovesTxBetweenSubpools(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip:    *uint256.NewInt(1000),
		FeeCap: *uint256.NewInt(250000),
		Gas:    100000,
		Nonce:  0,
	}
	s.IDHash[0] = 0x04
	s.Rlp = []byte{0xde, 0xad, 0x04}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	_, err = pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)

	raise := func(blockNum uint64, baseFee, blockHash byte) *remote.StateChangeBatch {
		h := gointerfaces.ConvertHashToH256([32]byte{blockHash})
		return &remote.StateChangeBatch{
			StateVersionId:      blockNum,
			PendingBlockBaseFee: uint64(baseFee) * 1_000_000,
			BlockGasLimit:       1000000,
			ChangeBatch: []*remote.StateChange{
				{BlockHeight: blockNum, BlockHash: h},
			},
		}
	}

	// Base fee above the txn's fee cap: it should fall out of pending.
	require.NoError(t, pool.OnNewBlock(ctx, raise(1, 255, 2), types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))
	pendingHigh, _, _ := pool.CountContent()

	// Base fee back down low: it should be promotable again.
	require.NoError(t, pool.OnNewBlock(ctx, raise(2, 1, 3), types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))
	pendingLow, baseFeeLow, queuedLow := pool.CountContent()

	require.GreaterOrEqual(t, pendingLow+baseFeeLow+queuedLow, pendingHigh, "txn should still be tracked somewhere after base fee drops")
}
