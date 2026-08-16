package api

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
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

	data, err := transaction.EncodeEthereumTransaction(tx)
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
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.Equal(t, "missing execution payload", *resp.ValidationError)

	emptyPayload := &ExecutionPayloadV3{
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	resp, err = engine.NewPayloadV3(context.Background(), emptyPayload, []types.Hash{}, nil)
	require.Nil(t, resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing parent beacon block root")

	resp, err = engine.NewPayloadV3(context.Background(), emptyPayload, nil, &root)
	require.Nil(t, resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing versioned hashes")

	expected := testVersionedHash(1)
	mismatch := testVersionedHash(2)
	payload := &ExecutionPayloadV3{
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
		Withdrawals:   []*Withdrawal{},
	}
	resp, err = engine.NewPayloadV3(context.Background(), payload, []types.Hash{mismatch}, &root)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.Equal(t, errBlobHashMismatch.Error(), *resp.ValidationError)
}

func TestEngineAPIBlobRejectsPreCancunV3AsUnsupportedFork(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		Time:       0,
		BaseFee:    uint256.NewInt(1),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:  big.NewInt(0),
		LondonBlock:  big.NewInt(0),
		ShanghaiTime: big.NewInt(0),
		CancunTime:   big.NewInt(15_000),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	root := types.Hash{0x01}

	resp, err := engine.NewPayloadV3(context.Background(), &ExecutionPayloadV3{
		ParentHash:    ethCompatibleBlockHash(genesisBlock, cfg),
		BlockNumber:   hexutil.Uint64(1),
		Timestamp:     hexutil.Uint64(14_999),
		GasLimit:      hexutil.Uint64(30_000_000),
		BaseFeePerGas: hexutil.Uint64(1),
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}, []types.Hash{}, &root)
	require.Nil(t, resp)
	require.Error(t, err)
	var codedErr interface{ ErrorCode() int }
	require.ErrorAs(t, err, &codedErr)
	require.Equal(t, -38005, codedErr.ErrorCode())
	require.Contains(t, err.Error(), "pre-Cancun")
}

func TestEngineAPIBlobRejectsMissingPostCancunBlobFieldsAsInvalidParams(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		Time:       0,
		BaseFee:    uint256.NewInt(1),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:  big.NewInt(0),
		LondonBlock:  big.NewInt(0),
		ShanghaiTime: big.NewInt(0),
		CancunTime:   big.NewInt(15_000),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	root := types.Hash{0x01}

	resp, err := engine.NewPayloadV3(context.Background(), &ExecutionPayloadV3{
		ParentHash:    ethCompatibleBlockHash(genesisBlock, cfg),
		BlockNumber:   hexutil.Uint64(1),
		Timestamp:     hexutil.Uint64(15_000),
		GasLimit:      hexutil.Uint64(30_000_000),
		BaseFeePerGas: hexutil.Uint64(1),
		ExcessBlobGas: hexUint64Ptr(0),
		Withdrawals:   []*Withdrawal{},
	}, nil, &root)
	require.Nil(t, resp)
	require.Error(t, err)
	var codedErr interface{ ErrorCode() int }
	require.ErrorAs(t, err, &codedErr)
	require.Equal(t, -32602, codedErr.ErrorCode())
	require.Contains(t, err.Error(), "missing blob gas fields")
}

func TestEngineAPIBlobRejectsIntrinsicGasTooLowTransaction(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	tx := transaction.NewTx(&transaction.AccessListTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasPrice:   uint256.NewInt(7),
		Gas:        params.TxGas - 1,
		To:         &to,
		Value:      uint256.NewInt(0),
		AccessList: transaction.AccessList{},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		Transactions:  []hexutil.Bytes{raw},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, api.chainConfig, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, []types.Hash{}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "intrinsic gas too low", *resp.ValidationError)
}

func TestEngineAPIBlobRejectsBlobTxBelowBlockBlobFee(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(0),
		BlobHashes: []types.Hash{testVersionedHash(1)},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		Transactions:  []hexutil.Bytes{raw},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, api.chainConfig, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, tx.BlobHashes(), &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, errBlobTxFeeCapTooLow.Error(), *resp.ValidationError)
}

func TestEngineAPIBlobRejectsInvalidExcessBlobGasBeforeBlobFeeValidation(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{testVersionedHash(1)},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		Transactions:  []hexutil.Bytes{raw},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(^uint64(0)),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, api.chainConfig, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, tx.BlobHashes(), &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Contains(t, *resp.ValidationError, "incorrect excess blob gas")
}

func TestEngineAPIBlobRejectsWrappedNegativeExcessBlobGasUnderCancunWithoutHanging(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          0,
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(5 * transaction.BlobTxBlobGasPerBlob),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{testVersionedHash(1)},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	wrappedNegativeExcessBlobGas := ^uint64(0) - transaction.BlobTxBlobGasPerBlob + 1
	payload := &ExecutionPayloadV3{
		ParentHash:    ethCompatibleBlockHash(genesisBlock, cfg),
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(1),
		BaseFeePerGas: hexutil.Uint64(7),
		Transactions:  []hexutil.Bytes{raw},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(wrappedNegativeExcessBlobGas),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resultCh := make(chan struct {
		resp *PayloadStatusV1
		err  error
	}, 1)
	go func() {
		resp, err := engine.NewPayloadV3(context.Background(), payload, tx.BlobHashes(), &beaconRoot)
		resultCh <- struct {
			resp *PayloadStatusV1
			err  error
		}{resp: resp, err: err}
	}()

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.Equal(t, PayloadStatusInvalid, result.resp.Status)
		require.NotNil(t, result.resp.ValidationError)
		require.Contains(t, *result.resp.ValidationError, "incorrect excess blob gas")
	case <-time.After(2 * time.Second):
		t.Fatal("NewPayloadV3 hung on wrapped negative excess blob gas")
	}
}

func TestEngineAPIv4RejectsWrappedNegativeExcessBlobGasWithoutHanging(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          0,
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(5 * transaction.BlobTxBlobGasPerBlob),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		PectraTime:    big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{testVersionedHash(1)},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	wrappedNegativeExcessBlobGas := ^uint64(0) - transaction.BlobTxBlobGasPerBlob + 1
	requestsHash := executionRequestsHash(nil)
	payload := &ExecutionPayloadV4{
		ParentHash:    ethCompatibleBlockHash(genesisBlock, cfg),
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(1),
		BaseFeePerGas: hexutil.Uint64(7),
		Transactions:  []hexutil.Bytes{raw},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(wrappedNegativeExcessBlobGas),
	}
	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resultCh := make(chan struct {
		resp *PayloadStatusV1
		err  error
	}, 1)
	go func() {
		resp, err := engine.NewPayloadV4(context.Background(), payload, tx.BlobHashes(), &beaconRoot, nil)
		resultCh <- struct {
			resp *PayloadStatusV1
			err  error
		}{resp: resp, err: err}
	}()

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.Equal(t, PayloadStatusInvalid, result.resp.Status)
		require.NotNil(t, result.resp.ValidationError)
		require.Contains(t, *result.resp.ValidationError, "incorrect excess blob gas")
	case <-time.After(2 * time.Second):
		t.Fatal("NewPayloadV4 hung on wrapped negative excess blob gas")
	}
}

func TestEngineAPIBlobRejectsGasLimitBelowMinimum(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(params.MinGasLimit - 1),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		Transactions:  nil,
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, api.chainConfig, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, []types.Hash{}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "invalid gas limit below 5000", *resp.ValidationError)
}

func TestEngineAPIBlobRejectsGasLimitBelowMinimumWhenParentResolvedViaCurrentHeadHash(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      params.MinGasLimit,
		GasUsed:       0,
		Time:          0,
		BaseFee:       uint256.NewInt(7),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header:              genesisHeader,
		blk:                 genesisBlock,
		disableHeaderByHash: true,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engineV1 := NewEngineAPIV1(NewBlockChainAPI(api))
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	headHash := engineV1.currentHeadHash()
	beaconRoot := types.Hash{}

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(0),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(12),
		BaseFeePerGas: hexutil.Uint64(7),
		Transactions:  nil,
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, []types.Hash{}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "invalid gas limit below 5000", *resp.ValidationError)
}

func TestEngineAPIBlobRejectsOsakaPayloadAboveBlobGasAllowance(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	api.chainConfig.OsakaTime = big.NewInt(0)
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x99}
	hashes := make([]types.Hash, 10)
	for i := range hashes {
		hashes[i] = testVersionedHash(byte(i + 1))
	}

	resp, err := engine.NewPayloadV3(context.Background(), &ExecutionPayloadV3{
		Timestamp:     hexutil.Uint64(2),
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, hashes)},
		BlobGasUsed:   hexUint64Ptr(uint64(len(hashes)) * transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
		Withdrawals:   []*Withdrawal{},
	}, hashes, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "blob gas used 1310720 exceeds maximum allowance 1179648", *resp.ValidationError)
}

func TestEngineAPIBlobRejectsStaticExcessBlobGasFromZeroOnBlobsAboveTarget(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(7 * params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: ptrToUint64(0),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engineV1 := NewEngineAPIV1(NewBlockChainAPI(api))
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	headHash := engineV1.currentHeadHash()
	beaconRoot := types.Hash{0x99}
	expected := testVersionedHash(1)

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, []types.Hash{expected}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "incorrect excess blob gas: have 0, want 131072", *resp.ValidationError)
}

func TestEngineAPIBlobAcceptsOsakaReservePriceBoundaryPayload(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(108),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(6 * params.BlobTxBlobGasPerBlob),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engineV1 := NewEngineAPIV1(NewBlockChainAPI(api))
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	headHash := engineV1.currentHeadHash()
	beaconRoot := types.Hash{0x99}
	expected := testVersionedHash(1)
	expectedExcess := uint64(6 * params.BlobTxBlobGasPerBlob)
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(200),
		Gas:        21000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{expected},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	rawTx, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)

	payload := &ExecutionPayloadV3{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(95),
		Transactions:  []hexutil.Bytes{rawTx},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(expectedExcess),
	}
	blk, err := executionPayloadV3ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
	})

	resp, err := engine.NewPayloadV3(context.Background(), payload, []types.Hash{expected}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.Status)
}

func TestEngineAPIBlobForkchoiceRequiresState(t *testing.T) {
	engine := NewEngineAPIBlob(nil)

	resp, err := engine.ForkchoiceUpdatedV3(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.PayloadStatus.Status)
	require.Equal(t, "missing forkchoice state", *resp.PayloadStatus.ValidationError)
}

func TestEngineAPIPayloadRetrievalRejectsWrongForkVersion(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	v1 := NewEngineAPIV1(NewBlockChainAPI(api))
	v3 := NewEngineAPIBlob(NewBlockChainAPI(api))
	preCancunID := PayloadID{0x01}
	cancunID := PayloadID{0x02}
	api.engineOverlay.storeBuiltPayload(preCancunID, &engineBuiltPayload{
		v2: &ExecutionPayloadV2{},
	})
	api.engineOverlay.storeBuiltPayload(cancunID, &engineBuiltPayload{
		v2: &ExecutionPayloadV2{},
		v3: &ExecutionPayloadV3{},
	})

	respV3, err := v3.GetPayloadV3(context.Background(), preCancunID)
	require.Nil(t, respV3)
	requireEngineErrorCode(t, err, -38005)
	respV2, err := v1.GetPayloadV2(context.Background(), cancunID)
	require.Nil(t, respV2)
	requireEngineErrorCode(t, err, -38005)
}

func TestEngineAPIForkchoiceRejectsWrongForkAttributes(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	v1 := NewEngineAPIV1(NewBlockChainAPI(api))
	root := types.Hash{0x01}
	state := &ForkchoiceStateV1{HeadBlockHash: headHash}
	attrsV2 := &PayloadAttributesV2{
		PayloadAttributesV1:   PayloadAttributesV1{Timestamp: 2},
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &root,
	}
	resp, err := v1.ForkchoiceUpdatedV2(context.Background(), state, attrsV2)
	require.Nil(t, resp)
	requireEngineErrorCode(t, err, -38003)

	attrsV2.ParentBeaconBlockRoot = nil
	resp, err = v1.ForkchoiceUpdatedV2(context.Background(), state, attrsV2)
	require.Nil(t, resp)
	requireEngineErrorCode(t, err, -38005)

	api.chainConfig.CancunBlock = nil
	api.chainConfig.CancunTime = big.NewInt(100)
	api.chainConfig.PragueTime = nil
	api.chainConfig.OsakaTime = nil
	v3 := NewEngineAPIBlob(NewBlockChainAPI(api))
	state.HeadBlockHash = v3.v1().currentHeadHash()
	resp, err = v3.ForkchoiceUpdatedV3(context.Background(), state, &PayloadAttributesV3{
		Timestamp:             2,
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &root,
	})
	require.Nil(t, resp)
	requireEngineErrorCode(t, err, -38005)
}

func TestEngineAPIv4InputValidation(t *testing.T) {
	engine := NewEngineAPIv4(nil)
	root := types.Hash{0x01}

	resp, err := engine.NewPayloadV4(context.Background(), nil, nil, &root, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.Equal(t, "missing execution payload", *resp.ValidationError)

	expected := testVersionedHash(1)
	mismatch := testVersionedHash(2)
	payload := &ExecutionPayloadV4{
		Transactions:  []hexutil.Bytes{testBlobTxBytes(t, []types.Hash{expected})},
		BlobGasUsed:   hexUint64Ptr(transaction.BlobTxBlobGasPerBlob),
		ExcessBlobGas: hexUint64Ptr(0),
		Withdrawals:   []*Withdrawal{},
	}
	resp, err = engine.NewPayloadV4(context.Background(), payload, []types.Hash{mismatch}, &root, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.Equal(t, errBlobHashMismatch.Error(), *resp.ValidationError)

	forkchoiceResp, err := engine.ForkchoiceUpdatedV4(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, forkchoiceResp.PayloadStatus.Status)
	require.Equal(t, "missing forkchoice state", *forkchoiceResp.PayloadStatus.ValidationError)
}

func TestEngineAPIv4RejectsMalformedExecutionRequestsAsInvalidParams(t *testing.T) {
	engine := NewEngineAPIv4(nil)
	root := types.Hash{0x01}

	payload := &ExecutionPayloadV4{
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
		Withdrawals:   []*Withdrawal{},
	}
	executionRequests := []hexutil.Bytes{
		{ConsolidationRequestType, 0x01},
		{ConsolidationRequestType, 0x02},
	}

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &root, executionRequests)
	require.Nil(t, resp)
	require.Error(t, err)
	var codedErr interface{ ErrorCode() int }
	require.ErrorAs(t, err, &codedErr)
	require.Equal(t, -32602, codedErr.ErrorCode())
	require.Contains(t, err.Error(), "execution requests not in ascending type order")
}

func TestEngineAPIv4RejectsMissingBlobFieldsAsInvalidParams(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x42}

	resp, err := engine.NewPayloadV4(context.Background(), &ExecutionPayloadV4{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x55},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(1),
		BlockHash:     types.Hash{0x66},
		Transactions:  []hexutil.Bytes{},
		Withdrawals:   []*Withdrawal{},
	}, nil, &beaconRoot, nil)
	require.Nil(t, resp)
	require.Error(t, err)
	var codedErr interface{ ErrorCode() int }
	require.ErrorAs(t, err, &codedErr)
	require.Equal(t, -32602, codedErr.ErrorCode())
	require.Contains(t, err.Error(), "missing blob gas fields")
}

func TestEngineAPIBlobBuildsAndImportsMinimalPayloadV3(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIBlob(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x44}

	forkchoiceResp, err := engine.ForkchoiceUpdatedV3(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV3{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &beaconRoot,
	})
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, forkchoiceResp.PayloadStatus.Status)
	require.NotNil(t, forkchoiceResp.PayloadID)

	payloadResp, err := engine.GetPayloadV3(context.Background(), *forkchoiceResp.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, payloadResp.ExecutionPayload)
	require.NotNil(t, payloadResp.ExecutionPayload.BlobGasUsed)
	require.Equal(t, hexutil.Uint64(0), *payloadResp.ExecutionPayload.BlobGasUsed)
	require.NotNil(t, payloadResp.BlobsBundle)

	newPayloadResp, err := engine.NewPayloadV3(context.Background(), payloadResp.ExecutionPayload, []types.Hash{}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, payloadResp.ExecutionPayload.BlockHash, *newPayloadResp.LatestValidHash)

	latestBlock, err := NewBlockChainAPI(api).GetBlockByNumber(context.Background(), 1, false)
	require.NoError(t, err)
	require.Equal(t, avmtypes.FromastHash(payloadResp.ExecutionPayload.BlockHash), latestBlock["hash"])

	byHash, err := NewBlockChainAPI(api).GetBlockByHash(context.Background(), avmtypes.FromastHash(payloadResp.ExecutionPayload.BlockHash), false)
	require.NoError(t, err)
	require.Equal(t, avmtypes.FromastHash(payloadResp.ExecutionPayload.BlockHash), byHash["hash"])
}

func TestEngineAPIv4BuildsAndImportsMinimalPayloadV4(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x55}
	targetBlobs := hexutil.Uint64(1)

	forkchoiceResp, err := engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV4{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x33),
		SuggestedFeeRecipient: types.Address{0x66},
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &beaconRoot,
		TargetBlobsPerBlock:   &targetBlobs,
	})
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, forkchoiceResp.PayloadStatus.Status)
	require.NotNil(t, forkchoiceResp.PayloadID)

	payloadResp, err := engine.GetPayloadV4(context.Background(), *forkchoiceResp.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, payloadResp.ExecutionPayload)
	require.Empty(t, payloadResp.ExecutionRequests)

	otherBeaconRoot := types.Hash{0x77}
	secondResp, err := engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV4{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x33),
		SuggestedFeeRecipient: types.Address{0x66},
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &otherBeaconRoot,
		TargetBlobsPerBlock:   &targetBlobs,
	})
	require.NoError(t, err)
	require.NotNil(t, secondResp.PayloadID)
	require.NotEqual(t, *forkchoiceResp.PayloadID, *secondResp.PayloadID)

	thirdResp, err := engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV4{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x34),
		SuggestedFeeRecipient: types.Address{0x66},
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &beaconRoot,
		TargetBlobsPerBlock:   &targetBlobs,
	})
	require.NoError(t, err)
	require.NotNil(t, thirdResp.PayloadID)
	require.NotEqual(t, *forkchoiceResp.PayloadID, *thirdResp.PayloadID)

	newPayloadResp, err := engine.NewPayloadV4(context.Background(), payloadResp.ExecutionPayload, nil, &beaconRoot, payloadResp.ExecutionRequests)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
}

func newEnginePayloadTestAPI() (*API, types.Hash) {
	genesisHeader := &block.Header{
		Number:        uint256.NewInt(0),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		BerlinBlock:   big.NewInt(0),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	return api, engine.currentHeadHash()
}
