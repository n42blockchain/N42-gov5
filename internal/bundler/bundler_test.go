// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package bundler

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

func makeTestOp(sender types.Address, nonce uint64) *vm.UserOperation {
	return &vm.UserOperation{
		Sender:               sender,
		Nonce:                uint256.NewInt(nonce),
		CallData:             []byte{0x01, 0x02},
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(100000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000),
		MaxPriorityFeePerGas: uint256.NewInt(100000000),
		Signature:            []byte{0xff},
	}
}

// =============================================================================
// Mempool Tests
// =============================================================================

func TestUserOpPool_AddAndGet(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)
	sender := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	op := makeTestOp(sender, 0)

	hash, err := pool.Add(op)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if hash == (types.Hash{}) {
		t.Fatal("hash should not be zero")
	}

	got, ok := pool.Get(hash)
	if !ok {
		t.Fatal("Get returned false")
	}
	if got.Sender != sender {
		t.Fatalf("sender mismatch: want %s, got %s", sender, got.Sender)
	}
	if pool.Count() != 1 {
		t.Fatalf("count should be 1, got %d", pool.Count())
	}
}

func TestUserOpPool_DuplicateRejection(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)
	op := makeTestOp(types.HexToAddress("0xaabb"), 0)

	_, err := pool.Add(op)
	if err != nil {
		t.Fatalf("first Add failed: %v", err)
	}
	_, err = pool.Add(op)
	if err != ErrDuplicateOp {
		t.Fatalf("expected ErrDuplicateOp, got %v", err)
	}
}

func TestUserOpPool_FullRejection(t *testing.T) {
	pool := NewUserOpPool(2, vm.EntryPointV07, 1)

	_, _ = pool.Add(makeTestOp(types.HexToAddress("0x01"), 0))
	_, _ = pool.Add(makeTestOp(types.HexToAddress("0x02"), 0))
	_, err := pool.Add(makeTestOp(types.HexToAddress("0x03"), 0))
	if err != ErrPoolFull {
		t.Fatalf("expected ErrPoolFull, got %v", err)
	}
}

func TestUserOpPool_Remove(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)
	op := makeTestOp(types.HexToAddress("0xdead"), 0)

	hash, _ := pool.Add(op)
	pool.Remove(hash)
	if pool.Count() != 0 {
		t.Fatalf("count should be 0 after remove, got %d", pool.Count())
	}
	_, ok := pool.Get(hash)
	if ok {
		t.Fatal("Get should return false after remove")
	}
}

func TestUserOpPool_Pending(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)

	// Add ops with different gas prices.
	op1 := makeTestOp(types.HexToAddress("0x01"), 0)
	op1.MaxFeePerGas = uint256.NewInt(100)
	op2 := makeTestOp(types.HexToAddress("0x02"), 0)
	op2.MaxFeePerGas = uint256.NewInt(200)

	pool.Add(op1)
	pool.Add(op2)

	pending := pool.Pending(10)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	// First should have higher gas price.
	if pending[0].MaxFeePerGas.Cmp(pending[1].MaxFeePerGas) < 0 {
		t.Fatal("pending should be sorted by gas price descending")
	}
}

func TestUserOpPool_Clear(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)
	pool.Add(makeTestOp(types.HexToAddress("0x01"), 0))
	pool.Add(makeTestOp(types.HexToAddress("0x02"), 0))
	pool.Clear()
	if pool.Count() != 0 {
		t.Fatalf("count should be 0 after clear, got %d", pool.Count())
	}
}

func TestUserOpHash_Deterministic(t *testing.T) {
	op := makeTestOp(types.HexToAddress("0xdead"), 42)
	ep := vm.EntryPointV07

	h1 := UserOpHash(op, ep, 1)
	h2 := UserOpHash(op, ep, 1)
	if h1 != h2 {
		t.Fatal("hash should be deterministic")
	}

	// Different chain ID should produce different hash.
	h3 := UserOpHash(op, ep, 2)
	if h1 == h3 {
		t.Fatal("different chain ID should produce different hash")
	}
}

// =============================================================================
// Validator Tests
// =============================================================================

func TestValidateStatic_ValidOp(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	if err := v.ValidateStatic(op, vm.EntryPointV07); err != nil {
		t.Fatalf("valid op should pass: %v", err)
	}
}

func TestValidateStatic_ZeroSender(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.Address{}, 0)
	if err := v.ValidateStatic(op, vm.EntryPointV07); err == nil {
		t.Fatal("zero sender should fail")
	}
}

func TestValidateStatic_ZeroGas(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	op.CallGasLimit = uint256.NewInt(0)
	if err := v.ValidateStatic(op, vm.EntryPointV07); err == nil {
		t.Fatal("zero gas should fail")
	}
}

func TestValidateStatic_EmptySignature(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	op.Signature = nil
	if err := v.ValidateStatic(op, vm.EntryPointV07); err == nil {
		t.Fatal("empty signature should fail")
	}
}

func TestValidateStatic_PriorityExceedsFee(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	op.MaxPriorityFeePerGas = uint256.NewInt(2000000000) // > MaxFeePerGas
	if err := v.ValidateStatic(op, vm.EntryPointV07); err == nil {
		t.Fatal("priority > fee should fail")
	}
}

func TestValidateStatic_UnsupportedEntryPoint(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	if err := v.ValidateStatic(op, types.HexToAddress("0xbad")); err == nil {
		t.Fatal("unsupported entry point should fail")
	}
}

func TestValidateStatic_BadPaymasterData(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0xdead"), 0)
	op.PaymasterAndData = []byte{0x01, 0x02} // < 20 bytes
	if err := v.ValidateStatic(op, vm.EntryPointV07); err == nil {
		t.Fatal("short paymasterAndData should fail")
	}
}

// =============================================================================
// Bundle Builder Tests
// =============================================================================

func TestBundleBuilder_BuildCalldata(t *testing.T) {
	builder := NewBundleBuilder(vm.EntryPointV07)
	ops := []*vm.UserOperation{makeTestOp(types.HexToAddress("0x01"), 0)}
	beneficiary := types.HexToAddress("0xbeef")

	calldata := builder.BuildCalldata(ops, beneficiary)
	if calldata == nil {
		t.Fatal("calldata should not be nil")
	}
	// Check selector.
	if calldata[0] != 0x1f || calldata[1] != 0xad || calldata[2] != 0x94 || calldata[3] != 0x8c {
		t.Fatalf("wrong selector: %x", calldata[:4])
	}
	// Calldata should be at least selector (4) + offset (32) + beneficiary (32) + array len (32).
	if len(calldata) < 100 {
		t.Fatalf("calldata too short: %d bytes", len(calldata))
	}
}

func TestBundleBuilder_EmptyOps(t *testing.T) {
	builder := NewBundleBuilder(vm.EntryPointV07)
	calldata := builder.BuildCalldata(nil, types.Address{})
	if calldata != nil {
		t.Fatal("empty ops should return nil")
	}
}

func TestBundleBuilder_EstimateGas(t *testing.T) {
	builder := NewBundleBuilder(vm.EntryPointV07)
	ops := []*vm.UserOperation{makeTestOp(types.HexToAddress("0x01"), 0)}

	gas := builder.EstimateGas(ops)
	if gas.IsZero() {
		t.Fatal("gas estimate should not be zero")
	}
	// Expected: (100000 + 100000 + 50000) * 1.1 = 275000
	expected := uint256.NewInt(275000)
	if gas.Cmp(expected) != 0 {
		t.Fatalf("gas estimate: want %s, got %s", expected, gas)
	}
}

// =============================================================================
// Service Tests
// =============================================================================

func TestBundlerService_SendAndGet(t *testing.T) {
	svc := NewBundlerService(DefaultConfig(), 1)
	op := makeTestOp(types.HexToAddress("0xdead"), 0)

	hash, err := svc.SendUserOperation(op, vm.EntryPointV07)
	if err != nil {
		t.Fatalf("SendUserOperation failed: %v", err)
	}

	got, ok := svc.GetUserOperation(hash)
	if !ok {
		t.Fatal("GetUserOperation should find the op")
	}
	if got.Sender != op.Sender {
		t.Fatal("sender mismatch")
	}
}

func TestBundlerService_SupportedEntryPoints(t *testing.T) {
	svc := NewBundlerService(DefaultConfig(), 1)
	eps := svc.SupportedEntryPoints()
	if len(eps) != 2 {
		t.Fatalf("expected 2 entry points, got %d", len(eps))
	}
}

func TestBundlerService_EstimateGas(t *testing.T) {
	svc := NewBundlerService(DefaultConfig(), 1)
	op := makeTestOp(types.HexToAddress("0xdead"), 0)

	est, err := svc.EstimateUserOperationGas(op)
	if err != nil {
		t.Fatalf("EstimateUserOperationGas failed: %v", err)
	}
	if est.PreVerificationGas == 0 {
		t.Fatal("pre-verification gas should not be zero")
	}
}

func TestBundlerService_RejectInvalid(t *testing.T) {
	svc := NewBundlerService(DefaultConfig(), 1)
	op := makeTestOp(types.Address{}, 0) // Zero sender

	_, err := svc.SendUserOperation(op, vm.EntryPointV07)
	if err == nil {
		t.Fatal("should reject zero sender")
	}
}

// =============================================================================
// Config Tests
// =============================================================================

func TestConfig_IsSupportedEntryPoint(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.IsSupportedEntryPoint(vm.EntryPointV07) {
		t.Fatal("V07 should be supported")
	}
	if !cfg.IsSupportedEntryPoint(vm.EntryPointV06) {
		t.Fatal("V06 should be supported")
	}
	if cfg.IsSupportedEntryPoint(types.HexToAddress("0xbad")) {
		t.Fatal("random address should not be supported")
	}
}
