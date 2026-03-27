package internal

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestNewExecutionChainPolicyUsesBorTransferModeForBorConsensus(t *testing.T) {
	policy := newExecutionChainPolicy(nil, params.BorConsensus)
	if !policy.useBorTransfers {
		t.Fatal("expected Bor consensus to enable Bor transfer mode")
	}
	if policy.useBorSystemCallContext {
		t.Fatal("did not expect nil chain config to enable Bor system call mode")
	}
}

func TestExecutionChainPolicyUsesCoinbaseForBorSystemCalls(t *testing.T) {
	cfg := &params.ChainConfig{Bor: &params.BorConfig{}}
	policy := newExecutionChainPolicy(cfg, "")
	header := &block.Header{Coinbase: types.HexToAddress("0x1234")}
	msg := transaction.NewMessage(
		state.SystemAddress,
		nil,
		0, u256.Num0,
		21000, u256.Num0,
		nil, nil, nil, nil,
		nil, nil, false,
		true,
	)

	author, txContext := policy.systemCallContext(header, msg)
	if author == nil || *author != header.Coinbase {
		t.Fatalf("author = %v, want coinbase %s", author, header.Coinbase)
	}
	if txContext.Origin != (types.Address{}) {
		t.Fatalf("tx origin = %s, want zero address for Bor system call context", txContext.Origin)
	}
	if !policy.shouldIgnoreSystemCallError() {
		t.Fatal("expected Bor system call mode to ignore execution errors")
	}
}

func TestExecutionChainPolicyUsesSystemAddressForStandardSystemCalls(t *testing.T) {
	policy := newExecutionChainPolicy(&params.ChainConfig{}, "")
	header := &block.Header{Coinbase: types.HexToAddress("0x1234")}
	to := types.HexToAddress("0x9999")
	msg := transaction.NewMessage(
		state.SystemAddress,
		&to,
		0, u256.Num0,
		21000, u256.Num0,
		nil, nil, nil, nil,
		nil, nil, false,
		true,
	)

	author, txContext := policy.systemCallContext(header, msg)
	if author == nil || *author != state.SystemAddress {
		t.Fatalf("author = %v, want system address %s", author, state.SystemAddress)
	}
	if txContext.Origin != state.SystemAddress {
		t.Fatalf("tx origin = %s, want system address %s", txContext.Origin, state.SystemAddress)
	}
	if policy.shouldIgnoreSystemCallError() {
		t.Fatal("did not expect standard system call mode to ignore execution errors")
	}
}
