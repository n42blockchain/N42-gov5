package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
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
