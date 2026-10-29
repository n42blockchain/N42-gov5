package api

// engine_osaka_blobs_test.go covers the EIP-7594 Engine API surface:
// engine_getPayloadV5 (Osaka payloads with a BlobsBundleV2 of cell proofs),
// engine_getBlobsV2 (blobs with cell proofs, all or nothing) and the builder
// rule that a payload only includes blob transactions whose sidecar version
// matches the payload's fork.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/kzg"
)

func cellProofBundle(s *testBlobSidecars) *BlobsBundleV1 {
	bundle := &BlobsBundleV1{
		Commitments: []hexutil.Bytes{append(hexutil.Bytes(nil), s.commitment[:]...)},
		Blobs:       []hexutil.Bytes{append(hexutil.Bytes(nil), s.blob[:]...)},
	}
	for i := range s.cellProofs {
		bundle.Proofs = append(bundle.Proofs, append(hexutil.Bytes(nil), s.cellProofs[i][:]...))
	}
	return bundle
}

func TestEngineGetPayloadV5ServesOsakaPayloadsWithCellProofs(t *testing.T) {
	s := loadTestBlobSidecars(t)
	api, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))

	store := func(id PayloadID, ts uint64, bundle *BlobsBundleV1) {
		api.engineOverlay.storeBuiltPayload(id, &engineBuiltPayload{
			v1:                &ExecutionPayloadV1{Timestamp: hexutil.Uint64(ts)},
			v4:                &ExecutionPayloadV4{BlockNumber: 22_000_001, Timestamp: hexutil.Uint64(ts)},
			blobsBundle:       bundle,
			executionRequests: []hexutil.Bytes{},
		})
	}
	pragueID, osakaID := PayloadID{0x51}, PayloadID{0x52}
	store(pragueID, mainnetPragueTS+12, emptyBlobsBundle())
	store(osakaID, mainnetOsakaTS+12, cellProofBundle(s))

	resp, err := v4.GetPayloadV5(context.Background(), osakaID)
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionPayload)
	require.NotNil(t, resp.ExecutionRequests)
	require.NotNil(t, resp.BlobsBundle)
	require.Len(t, resp.BlobsBundle.Blobs, 1)
	require.Len(t, resp.BlobsBundle.Commitments, 1)
	require.Len(t, resp.BlobsBundle.Proofs, transaction.CellProofsPerBlob)
	require.Equal(t, hexutil.Bytes(s.cellProofs[127][:]), resp.BlobsBundle.Proofs[127])

	// getPayloadV5 is Osaka only; getPayloadV4 stays Prague only.
	_, err = v4.GetPayloadV5(context.Background(), pragueID)
	requireEngineErrorCode(t, err, -38005)
	_, err = v4.GetPayloadV4(context.Background(), osakaID)
	requireEngineErrorCode(t, err, -38005)

	_, err = v4.GetPayloadV5(context.Background(), PayloadID{0x53})
	require.Error(t, err)
}

// An Osaka payload must never hand a consensus client a bundle with one
// blob proof per blob: getPayloadV5 refuses such a bundle.
func TestEngineGetPayloadV5RejectsBlobProofBundle(t *testing.T) {
	s := loadTestBlobSidecars(t)
	api, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))
	id := PayloadID{0x54}
	api.engineOverlay.storeBuiltPayload(id, &engineBuiltPayload{
		v4: &ExecutionPayloadV4{BlockNumber: 22_000_001, Timestamp: hexutil.Uint64(mainnetOsakaTS + 12)},
		blobsBundle: &BlobsBundleV1{
			Commitments: []hexutil.Bytes{s.commitment[:]},
			Blobs:       []hexutil.Bytes{s.blob[:]},
			Proofs:      []hexutil.Bytes{s.blobProof[:]},
		},
	})
	_, err := v4.GetPayloadV5(context.Background(), id)
	require.Error(t, err)
}

func blobPoolWith(txs ...*transaction.Transaction) *mockEngineTxPool {
	pool := &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}}
	for i, tx := range txs {
		addr := types.Address{byte(i + 1)}
		pool.pending[addr] = append(pool.pending[addr], tx)
	}
	return pool
}

func TestEngineGetBlobsV2ReturnsCellProofsAllOrNothing(t *testing.T) {
	s := loadTestBlobSidecars(t)
	fx := apiXGetChainFixture(t)
	key, to := fx.SenderKeys[0], fx.Senders[1]
	chainID := fx.Config.ChainID
	vhash := kzg.CommitmentToVersionedHash(s.commitment)

	osakaAPI, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	osakaAPI.txspool = blobPoolWith(signedBlobTx(t, chainID, key, to, 0, s, s.v1()))
	v4 := NewEngineAPIv4(NewBlockChainAPI(osakaAPI))

	got, err := v4.GetBlobsV2(context.Background(), []types.Hash{vhash})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0])
	require.Equal(t, hexutil.Bytes(s.blob[:]), got[0].Blob)
	require.Len(t, got[0].CellProofs, transaction.CellProofsPerBlob)
	for i := range s.cellProofs {
		require.Equal(t, hexutil.Bytes(s.cellProofs[i][:]), got[0].CellProofs[i])
	}

	// Any missing blob makes the whole response null.
	got, err = v4.GetBlobsV2(context.Background(), []types.Hash{vhash, testVersionedHash(0x77)})
	require.NoError(t, err)
	require.Nil(t, got)

	// Requests above 128 hashes are too large.
	_, err = v4.GetBlobsV2(context.Background(), make([]types.Hash, 129))
	requireEngineErrorCode(t, err, -38004)

	// A blob held only with a version-0 (blob proof) sidecar is an older
	// version: null.
	v0API, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	v0API.txspool = blobPoolWith(signedBlobTx(t, chainID, key, to, 0, s, s.v0()))
	got, err = NewEngineAPIv4(NewBlockChainAPI(v0API)).GetBlobsV2(context.Background(), []types.Hash{vhash})
	require.NoError(t, err)
	require.Nil(t, got)

	// Before Osaka the method answers null, as geth does.
	pragueAPI, _ := newMainnetEngineTestAPI(t, mainnetPragueTS)
	pragueAPI.txspool = blobPoolWith(signedBlobTx(t, chainID, key, to, 0, s, s.v1()))
	got, err = NewEngineAPIv4(NewBlockChainAPI(pragueAPI)).GetBlobsV2(context.Background(), []types.Hash{vhash})
	require.NoError(t, err)
	require.Nil(t, got)
}

// A built Osaka payload's bundle also serves getBlobsV2 after the
// transaction left the pool.
func TestEngineGetBlobsV2ServesBuiltOsakaPayloadBundle(t *testing.T) {
	s := loadTestBlobSidecars(t)
	api, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	api.engineOverlay.storeBuiltPayload(PayloadID{0x55}, &engineBuiltPayload{
		v4:          &ExecutionPayloadV4{BlockNumber: 22_000_001, Timestamp: hexutil.Uint64(mainnetOsakaTS + 12)},
		blobsBundle: cellProofBundle(s),
	})
	got, err := NewEngineAPIv4(NewBlockChainAPI(api)).GetBlobsV2(context.Background(), []types.Hash{kzg.CommitmentToVersionedHash(s.commitment)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].CellProofs, transaction.CellProofsPerBlob)
}

func TestEngineCapabilitiesListOsakaBlobMethods(t *testing.T) {
	caps, err := NewEngineAPIV1(nil).ExchangeCapabilities(context.Background(), nil)
	require.NoError(t, err)
	require.Contains(t, caps, "engine_getPayloadV5")
	require.Contains(t, caps, "engine_getBlobsV2")
}

// The payload builder includes a blob transaction only with a sidecar of the
// payload fork's version: version 1 for an Osaka payload, 0 before.
func TestBlobTxIncludableInPayload(t *testing.T) {
	s := loadTestBlobSidecars(t)
	fx := apiXGetChainFixture(t)
	key, to, chainID := fx.SenderKeys[0], fx.Senders[1], fx.Config.ChainID

	v1 := signedBlobTx(t, chainID, key, to, 0, s, s.v1())
	v0 := signedBlobTx(t, chainID, key, to, 0, s, s.v0())
	bare := signedBlobTx(t, chainID, key, to, 0, s, nil)
	legacy := transaction.NewTx(&transaction.LegacyTx{})

	require.True(t, blobTxIncludableInPayload(v1, true))
	require.False(t, blobTxIncludableInPayload(v0, true))
	require.True(t, blobTxIncludableInPayload(v0, false))
	require.False(t, blobTxIncludableInPayload(v1, false))
	require.False(t, blobTxIncludableInPayload(bare, true))
	require.False(t, blobTxIncludableInPayload(bare, false))
	require.True(t, blobTxIncludableInPayload(legacy, true))
}
