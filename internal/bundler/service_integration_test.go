// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bundler

import (
	"errors"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// mockTxSubmitter implements TxSubmitter for testing.
type mockTxSubmitter struct {
	mu         sync.Mutex
	calls      []submitCall
	returnErr  error
}

type submitCall struct {
	entryPoint types.Address
	calldata   []byte
	gasLimit   uint64
}

func (m *mockTxSubmitter) SubmitBundleTx(entryPoint types.Address, calldata []byte, gasLimit uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, submitCall{
		entryPoint: entryPoint,
		calldata:   calldata,
		gasLimit:   gasLimit,
	})
	return m.returnErr
}

func (m *mockTxSubmitter) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockTxSubmitter) lastCall() submitCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

// Ensure mockTxSubmitter satisfies the interface.
var _ TxSubmitter = (*mockTxSubmitter)(nil)

func TestTryCreateBundle_WithSubmitter(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)

	submitter := &mockTxSubmitter{}
	svc.SetTxSubmitter(submitter)

	// Add a UserOperation to the pool.
	sender := types.HexToAddress("0x1111111111111111111111111111111111111111")
	op := &vm.UserOperation{
		Sender:               sender,
		Nonce:                uint256.NewInt(0),
		CallData:             []byte{0x01, 0x02, 0x03},
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(100000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000),
		MaxPriorityFeePerGas: uint256.NewInt(100000000),
		Signature:            []byte{0xff},
	}

	_, err := svc.pool.Add(op)
	if err != nil {
		t.Fatalf("failed to add op: %v", err)
	}

	if svc.pool.Count() != 1 {
		t.Fatalf("expected pool count 1, got %d", svc.pool.Count())
	}

	// Call tryCreateBundle.
	err = svc.tryCreateBundle()
	if err != nil {
		t.Fatalf("tryCreateBundle failed: %v", err)
	}

	// Verify submitter was called.
	if submitter.callCount() != 1 {
		t.Fatalf("expected submitter to be called once, got %d", submitter.callCount())
	}

	call := submitter.lastCall()
	if call.entryPoint != cfg.EntryPoints[0] {
		t.Fatalf("wrong entryPoint: got %s, want %s", call.entryPoint.Hex(), cfg.EntryPoints[0].Hex())
	}
	if len(call.calldata) == 0 {
		t.Fatal("calldata should not be empty")
	}
	if call.gasLimit == 0 {
		t.Fatal("gasLimit should not be zero")
	}

	// Ops should be removed from pool after successful bundle.
	if svc.pool.Count() != 0 {
		t.Fatalf("expected pool to be empty after bundle, got %d", svc.pool.Count())
	}
}

func TestTryCreateBundle_WithoutSubmitter(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)
	// Do NOT set TxSubmitter.

	sender := types.HexToAddress("0x2222222222222222222222222222222222222222")
	op := &vm.UserOperation{
		Sender:               sender,
		Nonce:                uint256.NewInt(0),
		CallData:             []byte{0x01},
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(100000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000),
		MaxPriorityFeePerGas: uint256.NewInt(100000000),
		Signature:            []byte{0xff},
	}

	_, err := svc.pool.Add(op)
	if err != nil {
		t.Fatalf("failed to add op: %v", err)
	}

	// Should NOT panic, just log a warning and retain ops for later retry.
	err = svc.tryCreateBundle()
	if err != nil {
		t.Fatalf("tryCreateBundle should not return error without submitter: %v", err)
	}

	// Ops should be retained in pool when no submitter is configured.
	if svc.pool.Count() != 1 {
		t.Fatalf("expected pool to retain 1 op without submitter, got %d", svc.pool.Count())
	}
}

func TestTryCreateBundle_SubmitterError(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)

	submitter := &mockTxSubmitter{
		returnErr: errors.New("tx submission failed"),
	}
	svc.SetTxSubmitter(submitter)

	sender := types.HexToAddress("0x3333333333333333333333333333333333333333")
	op := &vm.UserOperation{
		Sender:               sender,
		Nonce:                uint256.NewInt(0),
		CallData:             []byte{0x01},
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(100000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000),
		MaxPriorityFeePerGas: uint256.NewInt(100000000),
		Signature:            []byte{0xff},
	}

	_, err := svc.pool.Add(op)
	if err != nil {
		t.Fatalf("failed to add op: %v", err)
	}

	// tryCreateBundle should propagate the error.
	err = svc.tryCreateBundle()
	if err == nil {
		t.Fatal("expected error from submitter to propagate")
	}
	if err.Error() != "tx submission failed" {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Ops should NOT be removed from pool when submitter returns error.
	if svc.pool.Count() != 1 {
		t.Fatalf("expected pool to still have 1 op after submitter error, got %d", svc.pool.Count())
	}
}

func TestTryCreateBundle_EmptyPool(t *testing.T) {
	cfg := DefaultConfig()
	svc := NewBundlerService(cfg, 1)

	submitter := &mockTxSubmitter{}
	svc.SetTxSubmitter(submitter)

	// Empty pool should be a no-op.
	err := svc.tryCreateBundle()
	if err != nil {
		t.Fatalf("tryCreateBundle on empty pool should not error: %v", err)
	}
	if submitter.callCount() != 0 {
		t.Fatal("submitter should not be called for empty pool")
	}
}
