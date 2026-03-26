package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestExecutionMessagePolicySetsCheckNonceFromStatelessFlag(t *testing.T) {
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(1), nil, nil, nil, nil, nil, nil, false, false)

	newExecutionMessagePolicy(false, false).normalize(&msg)
	if !msg.CheckNonce() {
		t.Fatal("expected check nonce to be enabled for stateful execution")
	}

	newExecutionMessagePolicy(true, false).normalize(&msg)
	if msg.CheckNonce() {
		t.Fatal("expected check nonce to be disabled for stateless execution")
	}
}

func TestExecutionMessagePolicyClearsFreeFlagForZeroFeeMessageWhenEngineExists(t *testing.T) {
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, nil, nil, false, true)

	newExecutionMessagePolicy(false, true).normalize(&msg)
	if msg.IsFree() {
		t.Fatal("expected zero-fee message to be marked paid when a consensus engine is present")
	}
}

func TestExecutionMessagePolicyKeepsFreeFlagWithoutConsensusEngine(t *testing.T) {
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, nil, nil, false, true)

	newExecutionMessagePolicy(false, false).normalize(&msg)
	if !msg.IsFree() {
		t.Fatal("expected free flag to remain unchanged without a consensus engine")
	}
}

func TestApplyServiceTransactionPolicyMarksZeroFeeMessageFromDetector(t *testing.T) {
	sender := types.HexToAddress("0x1000000000000000000000000000000000000001")
	msg := transaction.NewMessage(sender, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, nil, nil, false, false)

	ApplyServiceTransactionPolicy(&msg, func(got types.Address) bool {
		return got == sender
	})
	if !msg.IsFree() {
		t.Fatal("expected zero-fee service transaction to be marked free")
	}
}

func TestApplyServiceTransactionPolicySkipsNonZeroFeeMessage(t *testing.T) {
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(1), uint256.NewInt(1), uint256.NewInt(1), nil, nil, nil, nil, false, false)

	ApplyServiceTransactionPolicy(&msg, func(types.Address) bool {
		t.Fatal("service detector should not be called for paid messages")
		return false
	})
	if msg.IsFree() {
		t.Fatal("expected paid message to remain non-free")
	}
}
