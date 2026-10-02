package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestApiXChainFixtureBuildsExecutedChain is the fixture's own smoke test: it
// asserts the real import path actually ran (blocks present, receipts
// recorded, the contract's log emitted) so later tests can trust the fixture
// without re-deriving these facts themselves.
//
// Note: this deliberately does NOT use rawdb.ReadReceiptByTxHash — see the
// doc comment on apiXReadReceiptByHash below for a real indexing defect that
// function has.
func TestApiXChainFixtureBuildsExecutedChain(t *testing.T) {
	fx := apiXGetChainFixture(t)

	require.Len(t, fx.Blocks, 5, "3 transfer blocks + 1 create block + 1 call block")
	require.Len(t, fx.ValueTransferTxs, 4)
	require.NotEqual(t, fx.ContractAddr, fx.Senders[0])

	head := fx.Chain.CurrentBlock()
	require.Equal(t, uint64(5), head.Number64().Uint64())

	// The contract-call block's receipt must show exactly one log (the LOG0
	// the tiny contract emits).
	rec := apiXReadReceiptByHash(t, fx, fx.CallTx.Hash())
	require.NotNil(t, rec)
	require.Len(t, rec.Logs, 1)
	require.Equal(t, fx.ContractAddr, rec.Logs[0].Address)

	// TxLookup must resolve every value-transfer tx to its block.
	err := fx.DB.View(context.Background(), func(tx kv.Tx) error {
		for _, txn := range fx.ValueTransferTxs {
			num, lerr := rawdb.ReadTxLookupEntry(tx, txn.Hash())
			if lerr != nil {
				return lerr
			}
			require.NotNil(t, num)
		}
		return nil
	})
	require.NoError(t, err)
}

// apiXReadReceiptByHash resolves a transaction's receipt the way
// TransactionAPI.GetTransactionReceipt does: ReadTransactionByHash for the
// block hash + in-block index, then BlockChain.GetReceipts(blockHash)[index].
//
// DEFECT FOUND (not fixed; outside this task's scope of test-only changes):
// modules/rawdb.ReadReceiptByTxHash mis-indexes receipts for blocks using the
// sentinel-padded tx range (TxAmount = len(txs)+2; real transactions live at
// BaseTxId+1 .. BaseTxId+TxAmount-2, see accessors_chain.go's body writers).
// Its scan loop walks i in [0, TxAmount) and reads modules.BlockTx at
// BaseTxId+i, so when it finds the real transaction at slot i (i==1 for a
// one-transaction block, since slot 0 is the leading sentinel) it indexes
// `receipts[i]` directly — but receipts is zero-based with exactly len(txs)
// entries, so `receipts[1]` is out of range for a 1-tx block. The
// `int(i) < len(receipts)` guard then makes the function silently return a
// nil receipt instead of the real one. Confirmed against this fixture's
// one-transaction contract-call block (BaseTxId=15, TxAmount=3):
// rawdb.ReadRawReceipts correctly returns 1 receipt with 1 log, but
// rawdb.ReadReceiptByTxHash returns (nil, 0, 0, nil) for that same
// transaction's hash. Any RPC/debug path that still calls
// ReadReceiptByTxHash directly (as opposed to the index-then-slice pattern
// TransactionAPI.GetTransactionReceipt uses) will under-report receipts for
// small blocks.
func apiXReadReceiptByHash(t *testing.T, fx *apiXChainFixture, txHash types.Hash) *block.Receipt {
	t.Helper()
	var result *block.Receipt
	err := fx.DB.View(context.Background(), func(tx kv.Tx) error {
		foundTx, blockHash, _, index, terr := rawdb.ReadTransactionByHash(tx, txHash)
		if terr != nil {
			return terr
		}
		if foundTx == nil {
			return nil
		}
		receipts, rerr := fx.Chain.GetReceipts(blockHash)
		if rerr != nil {
			return rerr
		}
		if len(receipts) <= int(index) {
			return nil
		}
		result = receipts[index]
		return nil
	})
	require.NoError(t, err)
	return result
}
