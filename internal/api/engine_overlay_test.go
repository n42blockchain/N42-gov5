package api

import (
	"encoding/json"
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
		BaseFeePerGas: hexBigFromUint64(0),
		LogsBloom:     make([]byte, block.BloomByteLength),
		Transactions:  []hexutil.Bytes{raw},
	})
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	if blk.TxHash() != wantRoot {
		t.Fatalf("TxHash() = %s, want %s", blk.TxHash(), wantRoot)
	}
}

func TestExecutionPayloadBaseFeeSupports256Bits(t *testing.T) {
	want := mustBigIntFromHex(t, "100000000000000000000")
	payload := ExecutionPayloadV1{
		BaseFeePerGas: (*hexutil.Big)(new(big.Int).Set(want)),
		LogsBloom:     make([]byte, block.BloomByteLength),
		Transactions:  []hexutil.Bytes{},
	}
	encoded, err := json.Marshal(&payload)
	if err != nil {
		t.Fatalf("marshal large base fee: %v", err)
	}
	var decoded ExecutionPayloadV1
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal large base fee: %v", err)
	}

	blk, err := executionPayloadV1ToBlock(&decoded)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	header := blockHeader(blk)
	if header == nil || header.BaseFee == nil || header.BaseFee.ToBig().Cmp(want) != 0 {
		t.Fatalf("block base fee = %v, want %v", header.BaseFee, want)
	}

	roundTrip := blockToExecutionPayloadV1(blk, nil)
	if roundTrip == nil || roundTrip.BaseFeePerGas == nil || roundTrip.BaseFeePerGas.ToInt().Cmp(want) != 0 {
		t.Fatalf("round-trip base fee = %v, want %v", roundTrip, want)
	}
}

func mustBigIntFromHex(t *testing.T, value string) *big.Int {
	t.Helper()
	decoded, ok := new(big.Int).SetString(value, 16)
	if !ok {
		t.Fatalf("invalid test integer %q", value)
	}
	return decoded
}

func TestExecutionPayloadRejectsInvalidLogsBloomLength(t *testing.T) {
	for _, size := range []int{0, block.BloomByteLength - 1, block.BloomByteLength + 1} {
		_, err := executionPayloadV1ToBlock(&ExecutionPayloadV1{
			BaseFeePerGas: hexBigFromUint64(0),
			LogsBloom:     make([]byte, size),
		})
		if err == nil {
			t.Fatalf("accepted logs bloom length %d", size)
		}
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
	if payload.BaseFeePerGas.ToInt().Uint64() != want {
		t.Fatalf("BaseFeePerGas = %d, want %d", payload.BaseFeePerGas.ToInt().Uint64(), want)
	}
}
