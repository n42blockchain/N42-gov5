package api

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	vmcore "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/params"
	"github.com/stretchr/testify/require"
)

func mustEncodeEthereumTx(t *testing.T, tx *transaction.Transaction) hexutil.Bytes {
	t.Helper()

	data, err := transaction.EncodeEthereumTransaction(tx)
	require.NoError(t, err)
	return hexutil.Bytes(data)
}

func TestValidateExecutionPayloadTransactionsRejectsBlobWithoutHashes(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 1, 0, 30_000_000)
	require.ErrorIs(t, err, errBlobTxMissingHashes)
}

func TestValidateExecutionPayloadTransactionsRejectsSetCodeEmptyAuthList(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		LondonBlock: big.NewInt(0),
		PragueTime:  big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(0),
		V:         uint256.NewInt(0),
		R:         uint256.NewInt(1),
		S:         uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 1, 0, 30_000_000)
	require.ErrorIs(t, err, errSetCodeTxEmptyAuthList)
}

func TestValidateExecutionPayloadTransactionsAcceptsPragueSetCodeTx(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		LondonBlock: big.NewInt(0),
		PragueTime:  big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21000 + fixedgas.PerEmptyAccountCost,
		To:        &to,
		Value:     uint256.NewInt(0),
		AuthList: transaction.AuthorizationList{
			{
				ChainID: 1,
				Address: types.HexToAddress("0x0000000000000000000000000000000000000001"),
				Nonce:   0,
				V:       uint256.NewInt(0),
				R:       uint256.NewInt(1),
				S:       uint256.NewInt(2),
			},
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(1),
		S: uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 7, 0, 30_000_000)
	require.NoError(t, err)
}

func TestValidateExecutionPayloadTransactionsAcceptsOsakaBlobTxViaForkInheritance(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:   big.NewInt(1),
		OsakaTime: big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(7),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{types.HexToHash("0x01")},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 0, 7, 0, 30_000_000)
	require.NoError(t, err)
}

func TestValidateExecutionPayloadTransactionsRejectsOsakaBlobTxAbovePerTxLimit(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:   big.NewInt(1),
		OsakaTime: big.NewInt(0),
	}
	hashes := make([]types.Hash, 7)
	for i := range hashes {
		hashes[i][0] = transaction.VersionedHashVersionKZG
		hashes[i][len(hashes[i])-1] = byte(i + 1)
	}
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(7),
		Gas:        50_000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: hashes,
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 0, 7, 0, 30_000_000)
	require.ErrorIs(t, err, errBlobTxTooManyBlobs)
}

func TestValidateExecutionPayloadTransactionsRejectsPragueFloorGas(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		BerlinBlock:   big.NewInt(0),
		IstanbulBlock: big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		PragueTime:    big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.AccessListTx{
		ChainID:  uint256.NewInt(1),
		Nonce:    1,
		GasPrice: uint256.NewInt(1),
		Gas:      23000,
		To:       &to,
		Value:    uint256.NewInt(0),
		Data:     make([]byte, 100),
		V:        uint256.NewInt(0),
		R:        uint256.NewInt(1),
		S:        uint256.NewInt(1),
	})
	for i := range tx.Data() {
		tx.Data()[i] = 0x01
	}

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 0, 0, 30_000_000)
	require.ErrorIs(t, err, errFloorDataGasTooLow)
}

func TestValidateExecutionPayloadTransactionsRejectsOsakaGasLimitCap(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:   big.NewInt(1),
		OsakaTime: big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(1),
		Gas:      fixedgas.MaxTxnGasLimit + 1,
		To:       &to,
		Value:    uint256.NewInt(0),
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(1),
		S:        uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 0, 0, 30_000_000)
	require.ErrorIs(t, err, errTxGasLimitTooHigh)
}

func TestValidateExecutionPayloadTransactionsRejectsOversizedInitCode(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		ShanghaiBlock: big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(1),
		Gas:      1_000_000,
		To:       nil,
		Value:    uint256.NewInt(0),
		Data:     make([]byte, fixedgas.MaxInitCodeSize+1),
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(1),
		S:        uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 0, 0, 30_000_000)
	require.ErrorIs(t, err, errInitCodeSizeExceeded)
}

func TestValidateExecutionPayloadTransactionsRejectsTxGasAboveBlockGasLimit(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(1),
		Gas:      21_001,
		To:       &to,
		Value:    uint256.NewInt(0),
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(1),
		S:        uint256.NewInt(1),
	})

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 7, 0, 21_000)
	require.ErrorIs(t, err, internalcore.ErrGasLimitReached)
}

func TestValidateExecutionPayloadTransactionsRejectsBlobTxBelowBlockBlobFee(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
	}
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

	err := validateExecutionPayloadTransactions([]hexutil.Bytes{mustEncodeEthereumTx(t, tx)}, cfg, 1, 1, 1, 0, 30_000_000)
	require.ErrorIs(t, err, errBlobTxFeeCapTooLow)
}

func TestValidateExecutionPayloadHeaderRejectsGasLimitBelowMinimum(t *testing.T) {
	t.Parallel()

	parent := &block.Header{
		Number:   uint256.NewInt(0),
		GasLimit: 30_000_000,
	}
	header := &block.Header{
		Number:   uint256.NewInt(1),
		GasLimit: params.MinGasLimit - 1,
	}

	err := validateExecutionPayloadHeader(header, parent, nil)
	require.ErrorContains(t, err, "invalid gas limit")
}

func TestValidateExecutionPayloadHeaderRejectsGasUsedAboveGasLimit(t *testing.T) {
	t.Parallel()

	header := &block.Header{
		Number:   uint256.NewInt(1),
		GasLimit: 10,
		GasUsed:  11,
	}

	err := validateExecutionPayloadHeader(header, nil, nil)
	require.EqualError(t, err, "invalid gasUsed: have 11, gasLimit 10")
}

func TestValidateExecutionPayloadHeaderRejectsIncorrectExcessBlobGasForOsakaSchedule(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		PectraTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	parent := &block.Header{
		Number:        uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   7 * params.BlobTxBlobGasPerBlob,
		ExcessBlobGas: 0,
	}
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          2,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   params.BlobTxBlobGasPerBlob,
		ExcessBlobGas: 0,
	}

	err := validateExecutionPayloadHeader(header, parent, cfg)
	require.EqualError(t, err, "incorrect excess blob gas: have 0, want 131072")
}

func TestValidateExecutionPayloadHeaderAcceptsOsakaReservePriceExcessBlobGas(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		PectraTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	parent := &block.Header{
		Number:        uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(108),
		BlobGasUsed:   0,
		ExcessBlobGas: 6 * params.BlobTxBlobGasPerBlob,
	}
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          2,
		BaseFee:       uint256.NewInt(95),
		BlobGasUsed:   params.BlobTxBlobGasPerBlob,
		ExcessBlobGas: 6 * params.BlobTxBlobGasPerBlob,
	}

	err := validateExecutionPayloadHeader(header, parent, cfg)
	require.NoError(t, err)
}

func TestValidateExecutionPayloadHeaderAcceptsZeroExcessBlobGasBelowTargetEvenWithReservePrice(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		PectraTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	parent := &block.Header{
		Number:        uint256.NewInt(0),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          1,
		BaseFee:       uint256.NewInt(108),
		BlobGasUsed:   0,
		ExcessBlobGas: 3 * params.BlobTxBlobGasPerBlob,
	}
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      30_000_000,
		GasUsed:       0,
		Time:          2,
		BaseFee:       uint256.NewInt(95),
		BlobGasUsed:   params.BlobTxBlobGasPerBlob,
		ExcessBlobGas: 0,
	}

	err := validateExecutionPayloadHeader(header, parent, cfg)
	require.NoError(t, err)
}

func TestValidateExecutionPayloadBlockRLPSizeRejectsOsakaOversize(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:   big.NewInt(1),
		OsakaTime: big.NewInt(0),
	}
	blk := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       1,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Extra:      make([]byte, vmcore.MaxRLPBlockSizeFusaka+1),
	}, nil)

	err := validateExecutionPayloadBlockRLPSize(blk, nil, cfg, enginePayloadHashOptions{})
	require.ErrorIs(t, err, vmcore.ErrRLPBlockSizeTooLarge)
}

func TestValidateExecutionRequestsAcceptsFlattenedGroupedEncoding(t *testing.T) {
	t.Parallel()

	payload := &ExecutionPayloadV4{
		DepositRequests:       make([]DepositRequest, 2),
		WithdrawalRequests:    make([]WithdrawalRequest, 1),
		ConsolidationRequests: make([]ConsolidationRequest, 1),
	}
	requests := []hexutil.Bytes{
		append([]byte{DepositRequestType}, make([]byte, vmcore.DepositRequestSize*2)...),
		append([]byte{WithdrawalRequestType}, make([]byte, vmcore.WithdrawalRequestSize)...),
		append([]byte{ConsolidationRequestType}, make([]byte, vmcore.ConsolidationRequestSize)...),
	}

	require.NoError(t, validateExecutionRequests(requests, payload))
}

func TestValidateExecutionRequestsAcceptsFlattenedGroupedEncodingWithoutLegacyPayloadFields(t *testing.T) {
	t.Parallel()

	payload := &ExecutionPayloadV4{}
	requests := []hexutil.Bytes{
		append([]byte{DepositRequestType}, make([]byte, vmcore.DepositRequestSize)...),
	}

	require.NoError(t, validateExecutionRequests(requests, payload))
}

func TestValidateExecutionRequestsRejectsTruncatedGroupedEncoding(t *testing.T) {
	t.Parallel()

	// When payload-local arrays are populated, truncated encoding is rejected.
	payload := &ExecutionPayloadV4{
		DepositRequests: make([]DepositRequest, 1), // non-empty: triggers size validation
	}
	requests := []hexutil.Bytes{
		append([]byte{DepositRequestType}, make([]byte, vmcore.DepositRequestSize-1)...),
	}

	err := validateExecutionRequests(requests, payload)
	require.ErrorIs(t, err, errInvalidRequestEncoding)
}

func TestValidateExecutionRequestsAcceptsOpaqueWhenPayloadArraysAbsent(t *testing.T) {
	t.Parallel()

	// When payload-local arrays are empty (opaque mode per EIP-7685),
	// requests are accepted without size validation.
	payload := &ExecutionPayloadV4{} // empty arrays
	requests := []hexutil.Bytes{
		append([]byte{DepositRequestType}, make([]byte, 17)...), // arbitrary size
	}

	err := validateExecutionRequests(requests, payload)
	require.NoError(t, err)
}

func TestExecutionPayloadBlockRLPSizeWrapsTypedTransactions(t *testing.T) {
	t.Parallel()

	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		ShanghaiBlock: big.NewInt(0),
		PragueTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	tx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21000 + fixedgas.PerEmptyAccountCost,
		To:        &to,
		Value:     uint256.NewInt(0),
		AuthList: transaction.AuthorizationList{
			{
				ChainID: 1,
				Address: types.HexToAddress("0x0000000000000000000000000000000000000001"),
				Nonce:   0,
				V:       uint256.NewInt(0),
				R:       uint256.NewInt(1),
				S:       uint256.NewInt(2),
			},
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(1),
		S: uint256.NewInt(1),
	})
	rawTx := mustEncodeEthereumTx(t, tx)
	blk := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       1,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}, nil)

	size, err := executionPayloadBlockRLPSize(blk, []hexutil.Bytes{rawTx}, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        []*Withdrawal{},
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	err = rlp.Encode(&buf, []interface{}{
		enginePayloadRLPHeader(blk.Header().(*block.Header), cfg, enginePayloadHashOptions{
			includeWithdrawals: true,
			withdrawals:        []*Withdrawal{},
		}),
		[][]byte{rawTx},
		[]interface{}{},
		[]*Withdrawal{},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(buf.Len()), size)
}

func TestEngineAPIv4RejectsOversizedOsakaPayload(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1),
		LondonBlock:   big.NewInt(0),
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		PragueTime:    big.NewInt(0),
		PectraTime:    big.NewInt(0),
		OsakaTime:     big.NewInt(0),
	}
	genesisHeader := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       1,
		BaseFee:    uint256.NewInt(1),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
		config: cfg,
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	beaconRoot := types.Hash{0x01}
	payload := &ExecutionPayloadV4{
		ParentHash:    ethCompatibleBlockHash(genesisBlock, cfg),
		FeeRecipient:  types.Address{0x02},
		StateRoot:     types.Hash{0x03},
		ReceiptsRoot:  types.Hash{0x04},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x05},
		BlockNumber:   1,
		GasLimit:      hexutil.Uint64(genesisHeader.GasLimit),
		GasUsed:       0,
		Timestamp:     2,
		ExtraData:     make([]byte, vmcore.MaxRLPBlockSizeFusaka+1),
		BaseFeePerGas: 1,
		Transactions:  []hexutil.Bytes{},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)
	requestsHash := executionRequestsHash(nil)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &beaconRoot, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.ValidationError)
	require.Contains(t, *resp.ValidationError, vmcore.ErrRLPBlockSizeTooLarge.Error())
}
