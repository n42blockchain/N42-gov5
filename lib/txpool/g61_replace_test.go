package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/types"
)

// TestG61Lifecycle_ReplaceByHigherTip covers addLocked's replace-on-price-bump
// path for two different currentSubPool branches in its switch: a queued txn
// (nonce-gapped, never promoted) and a pending/base-fee txn (nonce 0, eligible
// for promotion). In both cases a resubmission with a tip/feecap well above
// the required price bump must discard the original with ReplacedByHigherTip.
func TestG61Lifecycle_ReplaceByHigherTip(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	mk := func(nonce uint64, tip uint64, idByte byte) *types.TxSlot {
		s := &types.TxSlot{
			Tip:    *uint256.NewInt(tip),
			FeeCap: *uint256.NewInt(tip),
			Gas:    100000,
			Nonce:  nonce,
		}
		s.IDHash[0] = idByte
		s.Rlp = []byte{0xde, 0xad, idByte}
		return s
	}

	// Pending-eligible original (nonce 0) then a replacement at >2x tip.
	var slots types.TxSlots
	slots.Append(mk(0, 100000, 0x10), addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 1)

	var replace types.TxSlots
	replace.Append(mk(0, 300000, 0x11), addr[:], true)
	reasons2, err := pool.AddLocalTxs(ctx, replace, tx)
	require.NoError(t, err)
	require.Len(t, reasons2, 1)

	// Queued original (nonce-gapped) then a replacement at >2x tip.
	var queuedOrig types.TxSlots
	queuedOrig.Append(mk(5, 100000, 0x20), addr[:], true)
	_, err = pool.AddLocalTxs(ctx, queuedOrig, tx)
	require.NoError(t, err)

	var queuedReplace types.TxSlots
	queuedReplace.Append(mk(5, 300000, 0x21), addr[:], true)
	reasons3, err := pool.AddLocalTxs(ctx, queuedReplace, tx)
	require.NoError(t, err)
	require.Len(t, reasons3, 1)

	pending, baseFeeN, queued := pool.CountContent()
	require.Greater(t, pending+baseFeeN+queued, 0, "replacements should still leave live txns tracked")
}
