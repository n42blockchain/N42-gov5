package api

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

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
}
