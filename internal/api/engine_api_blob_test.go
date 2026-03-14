package api

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func hexUint64Ptr(v uint64) *hexutil.Uint64 {
	n := hexutil.Uint64(v)
	return &n
}

func testVersionedHash(suffix byte) types.Hash {
	var h types.Hash
	h[0] = transaction.VersionedHashVersionKZG
	h[len(h)-1] = suffix
	return h
}

func testBlobTxBytes(t *testing.T, hashes []types.Hash) hexutil.Bytes {
	t.Helper()

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: hashes,
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})

	data, err := tx.Marshal()
	require.NoError(t, err)
	return hexutil.Bytes(data)
}

func TestValidateBlobTransactions(t *testing.T) {
	expected := testVersionedHash(1)
	err := ValidateBlobTransactions([]hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})}, []types.Hash{expected})
	require.NoError(t, err)

	mismatch := testVersionedHash(2)
	err = ValidateBlobTransactions([]hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})}, []types.Hash{mismatch})
	require.ErrorIs(t, err, errBlobHashMismatch)
}

func TestEngineAPIBlobInputValidation(t *testing.T) {
	engine := NewEngineAPIBlob(nil)
	root := types.Hash{0x01}

	resp, err := engine.NewPayloadV3(context.Background(), nil, nil, &root)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status.Status)
	require.Equal(t, "missing execution payload", *resp.Status.ValidationError)

	emptyPayload := &ExecutionPayloadV3{
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	resp, err = engine.NewPayloadV3(context.Background(), emptyPayload, nil, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status.Status)
	require.Equal(t, "missing parent beacon block root", *resp.Status.ValidationError)

	expected := testVersionedHash(1)
	mismatch := testVersionedHash(2)
	payload := &ExecutionPayloadV3{
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	resp, err = engine.NewPayloadV3(context.Background(), payload, []types.Hash{mismatch}, &root)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status.Status)
	require.Equal(t, errBlobHashMismatch.Error(), *resp.Status.ValidationError)
}

func TestEngineAPIBlobForkchoiceRequiresState(t *testing.T) {
	engine := NewEngineAPIBlob(nil)

	resp, err := engine.ForkchoiceUpdatedV3(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.PayloadStatus.Status)
	require.Equal(t, "missing forkchoice state", *resp.PayloadStatus.ValidationError)
}

func TestEngineAPIv4InputValidation(t *testing.T) {
	engine := NewEngineAPIv4(nil)
	root := types.Hash{0x01}

	resp, err := engine.NewPayloadV4(context.Background(), nil, nil, &root, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status.Status)
	require.Equal(t, "missing execution payload", *resp.Status.ValidationError)

	expected := testVersionedHash(1)
	mismatch := testVersionedHash(2)
	payload := &ExecutionPayloadV4{
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	resp, err = engine.NewPayloadV4(context.Background(), payload, []types.Hash{mismatch}, &root, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status.Status)
	require.Equal(t, errBlobHashMismatch.Error(), *resp.Status.ValidationError)

	forkchoiceResp, err := engine.ForkchoiceUpdatedV4(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, forkchoiceResp.PayloadStatus.Status)
	require.Equal(t, "missing forkchoice state", *forkchoiceResp.PayloadStatus.ValidationError)
}
