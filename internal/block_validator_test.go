package internal

import (
	"context"
	"strings"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestValidateBodyRejectsNilBlockNumber(t *testing.T) {
	validator := &BlockValidator{
		bc:     &BlockChain{},
		config: &params.ChainConfig{},
	}
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	err := validator.ValidateBody(blk)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("ValidateBody() error = %v", err)
	}
}

func TestStateProcessorRejectsNilBlockNumber(t *testing.T) {
	processor := NewStateProcessor(&params.ChainConfig{}, &BlockChain{}, nil)
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	_, _, _, _, err := processor.Process(blk, nil, nil, nil, nil)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("Process() error = %v", err)
	}
}

func TestSysCallContractRejectsNilHeaderNumber(t *testing.T) {
	_, err := SysCallContract(types.Address{}, nil, params.ChainConfig{}, nil, &block.Header{}, nil)
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("SysCallContract() error = %v", err)
	}
}

func TestFinalizeBlockExecutionRejectsNilHeaderNumber(t *testing.T) {
	_, _, _, err := FinalizeBlockExecution(nil, &block.Header{}, nil, nil, &params.ChainConfig{}, nil, nil, nil, false)
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("FinalizeBlockExecution() error = %v", err)
	}
}

func TestApplyTransactionRejectsNilHeaderNumber(t *testing.T) {
	_, _, err := applyTransaction(&params.ChainConfig{}, nil, nil, nil, nil, &block.Header{}, nil, nil, nil, vm2.Config{})
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("applyTransaction() error = %v", err)
	}
}

func TestParallelApplyTxRejectsNilHeaderNumber(t *testing.T) {
	_, _, _, err := parallelApplyTx(&params.ChainConfig{}, nil, nil, nil, nil, &block.Header{}, nil, nil, vm2.Config{})
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("parallelApplyTx() error = %v", err)
	}
}

func TestValidateStateRejectsMismatchedStateRoot(t *testing.T) {
	validator := &BlockValidator{config: &params.ChainConfig{}}
	blk := testConcreteBlock(&block.Header{
		Number:      uint256.NewInt(1),
		Difficulty:  uint256.NewInt(1),
		BaseFee:     uint256.NewInt(0),
		GasUsed:     0,
		ReceiptHash: hash.DeriveSha(block.Receipts(nil)),
		Root:        types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
	}, &block.Body{})

	err := validator.ValidateState(blk, state.New(nil), nil, 0)
	if err == nil || !strings.Contains(err.Error(), "invalid merkle root") {
		t.Fatalf("ValidateState() error = %v, want invalid merkle root", err)
	}
}

func TestValidateBodyAcceptsShanghaiBlockBuiltWithLegacyTxRoot(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := transaction.NewTx(&transaction.AccessListTx{
		ChainID:  uint256.NewInt(1),
		Nonce:    1,
		GasPrice: uint256.NewInt(7),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(9),
		Data:     []byte{0x01},
		AccessList: transaction.AccessList{{
			Address: to,
			StorageKeys: []types.Hash{
				types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			},
		}},
		V: uint256.NewInt(27),
		R: uint256.NewInt(1),
		S: uint256.NewInt(2),
	})

	header := &block.Header{
		Number:      uint256.NewInt(0),
		Time:        0,
		Difficulty:  uint256.NewInt(1),
		BaseFee:     uint256.NewInt(0),
		TxHash:      hash.DeriveSha(transaction.Transactions([]*transaction.Transaction{tx})),
		ReceiptHash: hash.DeriveSha(block.Receipts(nil)),
	}
	blk := testConcreteBlock(header, &block.Body{Txs: []*transaction.Transaction{tx}})
	blockCache, _ := lru.New[types.Hash, *block.Block](1)
	validator := &BlockValidator{
		bc: &BlockChain{
			ctx:        context.Background(),
			ChainDB:    memdb.NewTestDB(t),
			blockCache: blockCache,
		},
		config: &params.ChainConfig{ShanghaiBlock: uint256.NewInt(0).ToBig()},
	}

	if err := validator.ValidateBody(blk); err != nil {
		t.Fatalf("ValidateBody() error = %v", err)
	}
}
