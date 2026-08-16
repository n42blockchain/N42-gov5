package api

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/params"
	"github.com/stretchr/testify/require"
)

func TestDecodeTransactionsRejectsBlobSidecar(t *testing.T) {
	t.Parallel()

	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21_000,
		To:         types.HexToAddress("0x1111111111111111111111111111111111111111"),
		Value:      new(uint256.Int),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []types.Hash{types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")},
		V:          new(uint256.Int),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
		Sidecar:    &transaction.BlobTxSidecar{},
	})
	raw, err := transaction.EncodeEthereumPooledTransaction(tx)
	require.NoError(t, err)

	_, err = decodeTransactions([]hexutil.Bytes{raw})
	require.EqualError(t, err, "unexpected blob sidecar in transaction at index 0")
}

func TestExecutionPayloadV1ToBlockUsesEthereumRawTransactionsRoot(t *testing.T) {
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     1,
		GasTipCap: uint256.NewInt(2),
		GasFeeCap: uint256.NewInt(30),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
		Data:      []byte{0x01, 0x02},
		V:         uint256.NewInt(1),
		R:         uint256.NewInt(2),
		S:         uint256.NewInt(3),
	})
	raw, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction() error = %v", err)
	}
	wantRoot := deriveEthereumListHash(ethereumRawTransactions{raw})

	blk, err := executionPayloadV1ToBlock(&ExecutionPayloadV1{
		Transactions: []hexutil.Bytes{raw},
	})
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	if blk.TxHash() != wantRoot {
		t.Fatalf("TxHash() = %s, want %s", blk.TxHash(), wantRoot)
	}
}

func TestBuildExecutionPayloadV1UsesEthereumEmptyReceiptRoot(t *testing.T) {
	payload := buildExecutionPayloadV1(nil, types.Hash{}, &PayloadAttributesV1{}, nil)
	if payload.ReceiptsRoot != hash.EmptyRootHash {
		t.Fatalf("ReceiptsRoot = %s, want %s", payload.ReceiptsRoot, hash.EmptyRootHash)
	}
	if payload.Transactions == nil {
		t.Fatal("Transactions is nil, want empty list")
	}
	if len(payload.Transactions) != 0 {
		t.Fatalf("len(Transactions) = %d, want 0", len(payload.Transactions))
	}
}

func TestBuildExecutionPayloadV1CalculatesNextBaseFee(t *testing.T) {
	cfg := &params.ChainConfig{LondonBlock: big.NewInt(0)}
	parentHeader := &block.Header{
		Number:     uint256.NewInt(0),
		GasLimit:   30_000_000,
		GasUsed:    0,
		BaseFee:    uint256.NewInt(params.InitialBaseFee),
		Difficulty: uint256.NewInt(0),
	}
	parent := block.NewBlock(parentHeader, nil)

	payload := buildExecutionPayloadV1(parent, types.Hash{}, &PayloadAttributesV1{
		Timestamp: 1,
	}, cfg)

	want := misc.CalcBaseFee(cfg, parentHeader).Uint64()
	if uint64(payload.BaseFeePerGas) != want {
		t.Fatalf("BaseFeePerGas = %d, want %d", uint64(payload.BaseFeePerGas), want)
	}
}
