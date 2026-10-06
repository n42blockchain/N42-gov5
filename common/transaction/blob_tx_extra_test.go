package transaction

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func newTestBlobTx() *BlobTx {
	return &BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         types.Address{1},
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(3),
		BlobHashes: []types.Hash{{0xAA}, {0xBB}},
	}
}

func TestBlobTxSignIsNil(t *testing.T) {
	tx := newTestBlobTx()
	require.Nil(t, tx.sign())
}

func TestBlobTxGetBlobFeeCapAndHashes(t *testing.T) {
	r := require.New(t)
	tx := newTestBlobTx()
	r.Equal(tx.BlobFeeCap, tx.GetBlobFeeCap())
	r.Equal(tx.BlobHashes, tx.GetBlobHashes())
}

func TestBlobTxSidecarLifecycle(t *testing.T) {
	r := require.New(t)
	tx := newTestBlobTx()
	r.Nil(tx.GetSidecar())
	r.False(tx.HasSidecar())

	// A sidecar with zero blobs still reports HasSidecar() == false.
	empty := &BlobTxSidecar{}
	tx.SetSidecar(empty)
	r.False(tx.HasSidecar())
	r.Same(empty, tx.GetSidecar())

	withBlobs := &BlobTxSidecar{Blobs: []Blob{{}}}
	tx.SetSidecar(withBlobs)
	r.True(tx.HasSidecar())
}

func TestVerifyBlobGas(t *testing.T) {
	r := require.New(t)
	r.NoError(VerifyBlobGas(0))
	r.NoError(VerifyBlobGas(MaxBlobGasPerBlock))
	err := VerifyBlobGas(MaxBlobGasPerBlock + 1)
	r.ErrorIs(err, ErrBlobGasLimitExceeded)
}
