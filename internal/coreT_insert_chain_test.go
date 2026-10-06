package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// coreTSignedTransfer signs a simple value-transfer transaction from
// f.Senders[from] to f.Senders[to], using nonce for the sender's nonce field
// (callers are responsible for keeping it consistent with on-chain state).
func coreTSignedTransfer(t *testing.T, f *coreTChainFixture, from, to int, nonce uint64) *transaction.Transaction {
	t.Helper()
	signer := transaction.NewLondonSigner(f.Config.ChainID)
	toAddr := f.Senders[to]
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(f.Config.ChainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       500_000,
		To:        &toAddr,
		Value:     uint256.NewInt(1_000),
	}
	txn := transaction.NewTx(inner)
	signed, err := transaction.SignTx(txn, signer, f.SenderKeys[from])
	require.NoError(t, err)
	return signed
}

// TestCoreTInsertChainHappyPath exercises the shared fixture's happy-path
// import: every block lands canonical, receipts/logs are retrievable, and
// the head tracks the last inserted block.
func TestCoreTInsertChainHappyPath(t *testing.T) {
	f := coreTGetChainFixture(t)

	last := f.Blocks[len(f.Blocks)-1]
	current := f.Chain.CurrentBlock()
	require.Equal(t, last.Hash(), current.Hash())

	for _, blk := range f.Blocks {
		num := blk.Header().(*block.Header).Number
		canonHash := f.Chain.GetCanonicalHash(num)
		require.Equal(t, blk.Hash(), canonHash, "block %s should be canonical", num)

		got, err := f.Chain.GetBlockByHash(blk.Hash())
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, blk.Hash(), got.Hash())

		receipts, err := f.Chain.GetReceipts(blk.Hash())
		require.NoError(t, err)
		require.Equal(t, len(blk.Transactions()), len(receipts))
	}

	// The contract-call block emitted exactly one LOG0.
	callBlock := f.Blocks[len(f.Blocks)-1]
	receipts, err := f.Chain.GetReceipts(callBlock.Hash())
	require.NoError(t, err)
	var sawLog bool
	for _, r := range receipts {
		if len(r.Logs) > 0 {
			sawLog = true
		}
	}
	require.True(t, sawLog, "contract call should have emitted a log")
}

// TestCoreTInsertChainErrKnownBlock re-inserts the current canonical head.
//
// DEFECT (documented, not fixed per task instructions): re-submitting the
// exact head block through InsertChain does NOT take the known-block
// fast-path skip. BlockValidator.ValidateBody gates ErrKnownBlock on
// HasBlockAndState(hash, number), and HasBlockAndState's state half
// (HasState) checks rawdb.IsCanonicalHash -- true for the head -- so in
// isolation ErrKnownBlock should fire and insertChain's "ignore already
// known" loop should consume the single-block chain with no execution and
// no error. Observed behavior instead: insertChain attempts to RE-EXECUTE
// the block's transactions against the already-advanced PlainState and
// fails with a spurious "nonce too low" error (reported as a bad block).
// This means any caller that naively retries a previously-accepted
// InsertChain call for its own last-known head (e.g. after a timeout with
// an ambiguous result) gets a hard, misleading failure instead of a no-op.
func TestCoreTInsertChainErrKnownBlock(t *testing.T) {
	// A fresh, non-shared fixture: reportBlock on the failed re-insert below
	// must not risk leaving any mark on the fixture every other test in this
	// package shares.
	f := coreTNewChainFixture(t)
	before := f.Chain.CurrentBlock().Hash()

	target := f.Blocks[len(f.Blocks)-1]
	_, err := f.Chain.InsertChain([]block.IBlock{target})
	require.Error(t, err, "documents the known-block re-insert defect; see comment above")
	require.Contains(t, err.Error(), "nonce too low")

	after := f.Chain.CurrentBlock().Hash()
	require.Equal(t, before, after, "even though the re-insert errors, the head must not move")
}

// TestCoreTInsertChainNonContiguous feeds two blocks that do not form a
// parent/child chain and expects InsertChain to reject it up front, before
// touching the database.
func TestCoreTInsertChainNonContiguous(t *testing.T) {
	f := coreTGetChainFixture(t)
	require.True(t, len(f.Blocks) >= 2)

	before := f.Chain.CurrentBlock().Hash()
	_, err := f.Chain.InsertChain([]block.IBlock{f.Blocks[0], f.Blocks[0]})
	require.Error(t, err)
	require.Contains(t, err.Error(), "non contiguous")

	after := f.Chain.CurrentBlock().Hash()
	require.Equal(t, before, after)
}

// TestCoreTInsertChainBadTxRoot builds a block carrying a real signed
// transaction but a corrupted TxHash, so ValidateBody's transaction-root
// check rejects it before execution (exercising the reportBlock bad-block
// logging path).
func TestCoreTInsertChainBadTxRoot(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	parent := bc.CurrentBlock()
	parentHeader := parent.Header().(*block.Header)

	txn := coreTSignedTransfer(t, f, 0, 1, 1)

	header := &block.Header{
		ParentHash: parent.Hash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + 10,
		Coinbase:   parentHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       parentHeader.Root,
		TxHash:     coreTBadHash(), // deliberately wrong: doesn't match txn's root
	}

	badBlock := block.NewBlockFromReceipt(header, []*transaction.Transaction{txn}, nil, nil, nil)
	// NewBlockFromReceipt recomputes TxHash itself from the given txs, so the
	// corruption has to be applied to the built header afterward.
	builtHeader := badBlock.Header().(*block.Header)
	builtHeader.TxHash = coreTBadHash()
	badBlock = badBlock.(*block.Block).WithSeal(builtHeader)

	before := bc.CurrentBlock().Hash()
	_, err := bc.InsertChain([]block.IBlock{badBlock})
	require.Error(t, err)
	require.Contains(t, err.Error(), "transaction root hash mismatch")
	require.Equal(t, before, bc.CurrentBlock().Hash())
}

// TestCoreTInsertChainBadParentHash builds a syntactically valid next block
// but points its ParentHash at an unknown hash: the single-block InsertChain
// contiguity pre-check cannot catch this (there's nothing to compare it
// against), so it must be rejected deeper in the pipeline (unknown ancestor).
func TestCoreTInsertChainBadParentHash(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	parent := bc.CurrentBlock()
	parentHeader := parent.Header().(*block.Header)

	header := &block.Header{
		ParentHash: coreTBadHash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + 10,
		Coinbase:   parentHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       parentHeader.Root,
	}
	header.TxHash = block.TxRootAt(nil, header.Time)

	badBlock := block.NewBlockFromReceipt(header, nil, nil, nil, nil)
	before := bc.CurrentBlock().Hash()
	_, err := bc.InsertChain([]block.IBlock{badBlock})
	// Either future-queued (unknown ancestor, nil error) or rejected
	// outright; either way the head must not advance onto an orphan.
	_ = err
	require.Equal(t, before, bc.CurrentBlock().Hash())
}

func coreTBadHash() (h types.Hash) {
	for i := range h {
		h[i] = 0xAB
	}
	return h
}
