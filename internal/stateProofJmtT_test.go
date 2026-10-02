package internal

// stateProofJmtT_test.go covers JMTStateProofProvider.AccountProof/
// StorageProof's live-tree path (resolveJMTRoot returning the zero hash for
// "latest"/"earliest-matches-current-root", so resolveProofTree short-
// circuits to nil and the provider reads straight off the commitment's live
// tree), plus jmtRootAtHeight's header-not-found error branch.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestJMTStateProofProviderAccountProofLiveTree(t *testing.T) {
	commit := newTestJMTCommitment()
	addr := types.HexToAddress("0x00000000000000000000000000000000001234")
	require.NoError(t, commit.UpdateAccount(addr, &account.StateAccount{
		Initialised: true,
		Nonce:       1,
		Balance:     *uint256.NewInt(100),
	}))
	require.NoError(t, commit.Flush())

	provider := NewJMTStateProofProvider(commit)

	// "latest": resolveJMTRoot returns the zero hash unconditionally, so
	// resolveProofTree takes the nil-tree shortcut straight to
	// commitment.GetAccountProof.
	nodes, err := provider.AccountProof(nil, addr, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, nodes)

	// An account that was never inserted still produces an exclusion proof
	// (jmt.GetProof does not error on a miss).
	other := types.HexToAddress("0x00000000000000000000000000000000005678")
	nodes2, err := provider.AccountProof(nil, other, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	require.NoError(t, err)
	_ = nodes2
}

func TestJMTStateProofProviderStorageProofLiveTree(t *testing.T) {
	commit := newTestJMTCommitment()
	addr := types.HexToAddress("0x00000000000000000000000000000000001234")
	slot := types.HexToHash("0x01")
	require.NoError(t, commit.UpdateStorage(addr, slot, uint256.NewInt(42)))
	require.NoError(t, commit.Flush())

	provider := NewJMTStateProofProvider(commit)
	nodes, err := provider.StorageProof(nil, addr, slot, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, nodes)
}

func TestJMTStateProofProviderAccountProofNilProviderOrCommitment(t *testing.T) {
	var nilProvider *JMTStateProofProvider
	nodes, err := nilProvider.AccountProof(nil, types.Address{}, jsonrpc.BlockNumberOrHash{})
	require.NoError(t, err)
	require.Nil(t, nodes)

	empty := &JMTStateProofProvider{}
	nodes, err = empty.StorageProof(nil, types.Address{}, types.Hash{}, jsonrpc.BlockNumberOrHash{})
	require.NoError(t, err)
	require.Nil(t, nodes)
}

func TestResolveJMTRootHistoricalHeightMissingHeaderErrors(t *testing.T) {
	commit := newTestJMTCommitment()
	db := memdb.NewTestDB(t)

	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		// Nothing written at height 42: ReadCanonicalHash returns the zero
		// hash and ReadHeader(tx, zeroHash, 42) finds nothing, so
		// resolveJMTRoot propagates jmtRootAtHeight's "header not found"
		// error for an explicit historical block number.
		_, err := resolveJMTRoot(tx, commit, jsonrpc.BlockNumberOrHashWithNumber(42))
		require.Error(t, err)
		return nil
	}))

	// A nil commitment short-circuits to the zero hash with no error,
	// regardless of the requested height.
	root, err := resolveJMTRoot(nil, nil, jsonrpc.BlockNumberOrHashWithNumber(42))
	require.NoError(t, err)
	require.Equal(t, types.Hash{}, root)
}
