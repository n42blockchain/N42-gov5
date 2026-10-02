package txpool

import (
	"context"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/types"
)

// TestSelection_YieldBest_GasLimitTooLargeSkip covers best()'s branch that
// skips (rather than returns) a transaction whose Gas meets or exceeds the
// current block gas limit, then still yields a smaller transaction behind it.
func TestSelection_YieldBest_GasLimitTooLargeSkip(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	blockGasLimit := pool.blockGasLimit.Load()

	var txSlots types.TxSlots
	// First: a transaction whose Gas is >= the block gas limit. It must be
	// skipped by best() without aborting the loop.
	huge := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    blockGasLimit,
		Nonce:  0,
	}
	huge.IDHash[0] = 0x01
	huge.Rlp = []byte{0xde, 0xad, 0x01}
	txSlots.Append(huge, addr[:], true)

	// Second: an ordinary, small transaction that should still be yielded.
	small := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  1,
	}
	small.IDHash[0] = 0x02
	small.Rlp = []byte{0xde, 0xad, 0x02}
	txSlots.Append(small, addr[:], true)

	reasons, err := pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 2)

	var out types.TxsRlp
	yielded := mapset.NewThreadUnsafeSet[[32]byte]()
	_, n, err := pool.YieldBest(16, &out, tx, 0, 10_000_000, 10_000_000, yielded)
	require.NoError(t, err)

	if yielded.Contains(huge.IDHash) {
		t.Error("the oversized-gas transaction must not be yielded")
	}
	if n == 0 {
		t.Fatal("expected the small transaction behind the skipped one to be yielded")
	}
}

// TestSelection_YieldBest_BlobGasSkip covers best()'s branch that skips a
// blob transaction when its blob gas would exceed availableBlobGas, while
// still yielding a non-blob transaction.
func TestSelection_YieldBest_BlobGasSkip(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	var txSlots types.TxSlots
	blobTx := &types.TxSlot{
		Tip:        *uint256.NewInt(300000),
		FeeCap:     *uint256.NewInt(300000),
		Gas:        100000,
		Nonce:      0,
		BlobHashes: []common.Hash{{0x01}},
	}
	blobTx.IDHash[0] = 0x03
	blobTx.Rlp = []byte{0xde, 0xad, 0x03}
	txSlots.Append(blobTx, addr[:], true)

	plainTx := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  1,
	}
	plainTx.IDHash[0] = 0x04
	plainTx.Rlp = []byte{0xde, 0xad, 0x04}
	txSlots.Append(plainTx, addr[:], true)

	reasons, err := pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 2)

	var out types.TxsRlp
	yielded := mapset.NewThreadUnsafeSet[[32]byte]()
	// availableBlobGas=0 means the blob transaction's required blob gas (1
	// blob's worth) exceeds what's available, so it must be skipped.
	_, n, err := pool.YieldBest(16, &out, tx, 0, 10_000_000, 0, yielded)
	require.NoError(t, err)

	if yielded.Contains(blobTx.IDHash) {
		t.Error("the blob transaction must not be yielded when availableBlobGas is insufficient")
	}
	if n == 0 {
		t.Fatal("expected the plain transaction behind the skipped blob transaction to be yielded")
	}
}
