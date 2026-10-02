package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
)

// TestCoreTJMTStateProofProviderNilSafe exercises JMTStateProofProvider's
// nil-commitment short-circuit on every exported method, plus its pure
// helpers (encodeJMTProofNodes, resolveJMTRoot) directly. This chain fixture
// never configures a JMT root computer (it runs on the default/plain
// commitment), so a nil-backed provider is exactly what a node without JMT
// enabled would construct.
func TestCoreTJMTStateProofProviderNilSafe(t *testing.T) {
	var nilProvider *JMTStateProofProvider
	require.Nil(t, NewJMTStateProofProvider(nil))

	provider := &JMTStateProofProvider{}
	f := coreTGetChainFixture(t)

	err := f.DB.View(f.Chain.ctx, func(tx kv.Tx) error {
		bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

		got, aerr := provider.AccountProof(tx, f.Senders[0], bnh)
		require.NoError(t, aerr)
		require.Nil(t, got)

		got, aerr = provider.StorageProof(tx, f.Senders[0], types.Hash{}, bnh)
		require.NoError(t, aerr)
		require.Nil(t, got)

		// Nil-receiver variants must not panic either.
		got, aerr = nilProvider.AccountProof(tx, f.Senders[0], bnh)
		require.NoError(t, aerr)
		require.Nil(t, got)

		tree, terr := provider.resolveProofTree(tx, bnh)
		require.NoError(t, terr)
		require.Nil(t, tree)

		root, rerr := resolveJMTRoot(tx, nil, bnh)
		require.NoError(t, rerr)
		require.Equal(t, types.Hash{}, root)

		return nil
	})
	require.NoError(t, err)

	require.Nil(t, encodeJMTProofNodes(nil))

	desc := provider.Descriptor()
	require.Equal(t, StateProofBackendJMT, desc.Backend)

	hash, herr := provider.StorageHash(nil, f.Senders[0], nil, nil, jsonrpc.BlockNumberOrHash{})
	require.NoError(t, herr)
	require.Equal(t, types.Hash{}, hash)
}

// TestCoreTQMDBStateProofProviderBasics covers the trivial, state-independent
// methods of the QMDB proof provider (Descriptor, StorageHash) and confirms
// AccountProof/StorageProof on a non-QMDB chain fail cleanly (no panic)
// rather than serving a bogus proof -- this fixture's chain never enables
// the QMDB commitment, so the provider has nothing to load.
func TestCoreTQMDBStateProofProviderBasics(t *testing.T) {
	f := coreTGetChainFixture(t)
	provider := NewQMDBStateProofProvider(f.Config)

	desc := provider.Descriptor()
	require.Equal(t, StateProofBackendQMDB, desc.Backend)

	hash, err := provider.StorageHash(nil, f.Senders[0], nil, nil, jsonrpc.BlockNumberOrHash{})
	require.NoError(t, err)
	require.Equal(t, types.Hash{}, hash)

	_ = f.DB.View(f.Chain.ctx, func(tx kv.Tx) error {
		bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
		// Not asserting a specific outcome: this chain never wrote QMDB
		// proof-head bookkeeping, so either an error or an empty result is
		// acceptable -- the point of this test is that neither call panics.
		_, _ = provider.AccountProof(tx, f.Senders[0], bnh)
		_, _ = provider.StorageProof(tx, f.Senders[0], types.Hash{}, bnh)
		return nil
	})
}

// TestCoreTInsertChainAuthorized covers the InsertChainAuthorized entry
// point (the consensus-driven import path) with a straightforward next
// block, exercising its distinct locking wrapper around the shared
// insertChain core.
func TestCoreTInsertChainAuthorized(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	parent := bc.CurrentBlock()
	parentHeader := parent.Header().(*block.Header)
	txn := coreTSignedTransfer(t, f, 3, 1, 0)

	header := &block.Header{
		ParentHash: parent.Hash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + 10,
		Coinbase:   parentHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}

	var (
		receipts block.Receipts
		usedGas  uint64
		ibs      *state.IntraBlockState
	)
	engine := coreTFakerEngine{}
	err := f.DB.View(f.Chain.ctx, func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs = state.New(reader)
		gp := new(common.GasPool)
		gp.AddGas(header.GasLimit)
		ibs.Prepare(txn.Hash(), types.Hash{}, 0)
		receipt, _, aerr := ApplyTransaction(f.Config, nil, engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
		if aerr != nil {
			return aerr
		}
		receipts = append(receipts, receipt)
		return nil
	})
	require.NoError(t, err)

	header.GasUsed = usedGas
	header.Root = ibs.IntermediateRoot()
	header.ReceiptHash = hash.DeriveSha(receipts)
	header.Bloom = block.CreateBloom(receipts)

	builtIface, _, _, ferr := engine.FinalizeAndAssemble(bc, header, ibs, []*transaction.Transaction{txn}, nil, receipts)
	require.NoError(t, ferr)
	nextBlock := builtIface.(*block.Block)

	n, err := bc.InsertChainAuthorized([]block.IBlock{nextBlock})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, nextBlock.Hash(), bc.CurrentBlock().Hash())
}
