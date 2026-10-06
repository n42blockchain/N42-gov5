// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bind

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	N42 "github.com/n42blockchain/N42"
	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/types"
)

const testBalanceOfABI = `[{
	"constant": true,
	"inputs": [{"name": "who", "type": "address"}],
	"name": "balanceOf",
	"outputs": [{"name": "", "type": "uint256"}],
	"type": "function"
}]`

// fakeContractCaller implements ContractCaller (and optionally
// PendingContractCaller) purely in memory for exercising BoundContract.Call's
// option-handling branches (pending vs. latest, no-code rejection, result
// unpacking).
type fakeContractCaller struct {
	callOutput []byte
	callErr    error
	code       []byte
	codeErr    error

	pendingSupported bool
	pendingCode      []byte
	pendingCodeErr   error
}

func (f *fakeContractCaller) CodeAt(ctx context.Context, contract types.Address, blockNumber *uint256.Int) ([]byte, error) {
	return f.code, f.codeErr
}

func (f *fakeContractCaller) CallContract(ctx context.Context, call N42.CallMsg, blockNumber *uint256.Int) ([]byte, error) {
	return f.callOutput, f.callErr
}

func (f *fakeContractCaller) PendingCodeAt(ctx context.Context, contract types.Address) ([]byte, error) {
	return f.pendingCode, f.pendingCodeErr
}

func (f *fakeContractCaller) PendingCallContract(ctx context.Context, call N42.CallMsg) ([]byte, error) {
	return f.callOutput, f.callErr
}

// pendingCaller wraps fakeContractCaller so it also satisfies
// PendingContractCaller; plain fakeContractCaller already implements both
// method sets, but we keep this for readability at call sites that only
// want to emphasize the pending path.
type pendingCaller = fakeContractCaller

func mustBalanceOfABI(t *testing.T) abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(testBalanceOfABI))
	if err != nil {
		t.Fatalf("abi.JSON: %v", err)
	}
	return parsed
}

func TestCallSuccess(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	method := parsed.Methods["balanceOf"]
	encodedOut, err := method.Outputs.Pack(big.NewInt(100))
	if err != nil {
		t.Fatalf("pack output: %v", err)
	}

	caller := &fakeContractCaller{callOutput: encodedOut, code: []byte{0x60}}
	c := NewBoundContract(types.Address{}, parsed, caller, nil, nil)

	var out []interface{}
	if err := c.Call(nil, &out, "balanceOf", types.Address{1}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("Call results len = %d, want 1", len(out))
	}
	got, ok := out[0].(*big.Int)
	if !ok || got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("Call result = %v, want 100", out[0])
	}
}

func TestCallNilOptsAndResultsDefault(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	method := parsed.Methods["balanceOf"]
	encodedOut, _ := method.Outputs.Pack(big.NewInt(1))

	caller := &fakeContractCaller{callOutput: encodedOut}
	c := NewBoundContract(types.Address{}, parsed, caller, nil, nil)

	// Passing nil for both opts and results must not panic, and should
	// still populate a freshly allocated results slice internally (we
	// can't observe that slice here, but a nil-safe call is the point).
	if err := c.Call(nil, nil, "balanceOf", types.Address{1}); err != nil {
		t.Fatalf("Call with nil opts/results: %v", err)
	}
}

func TestCallEmptyOutputRequiresCode(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	c := NewBoundContract(types.Address{}, parsed, &fakeContractCaller{callOutput: nil, code: nil}, nil, nil)

	var out []interface{}
	err := c.Call(nil, &out, "balanceOf", types.Address{1})
	if err != ErrNoCode {
		t.Fatalf("Call with empty output + no code: err = %v, want ErrNoCode", err)
	}
}

func TestCallPendingWithoutSupportFails(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	// plainCaller only implements ContractCaller, not PendingContractCaller.
	caller := plainCaller{}
	c := NewBoundContract(types.Address{}, parsed, caller, nil, nil)

	var out []interface{}
	err := c.Call(&CallOpts{Pending: true}, &out, "balanceOf", types.Address{1})
	if err != ErrNoPendingState {
		t.Fatalf("Call(Pending) without PendingContractCaller: err = %v, want ErrNoPendingState", err)
	}
}

func TestCallPendingEmptyOutputNoCode(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	caller := &fakeContractCaller{callOutput: nil, pendingCode: nil}
	c := NewBoundContract(types.Address{}, parsed, caller, nil, nil)

	var out []interface{}
	err := c.Call(&CallOpts{Pending: true}, &out, "balanceOf", types.Address{1})
	if err != ErrNoCode {
		t.Fatalf("Call(Pending) empty output + no pending code: err = %v, want ErrNoCode", err)
	}
}

func TestCallPackError(t *testing.T) {
	parsed := mustBalanceOfABI(t)
	c := NewBoundContract(types.Address{}, parsed, &fakeContractCaller{}, nil, nil)

	var out []interface{}
	// Wrong argument type/count should fail at the Pack step.
	if err := c.Call(nil, &out, "balanceOf"); err == nil {
		t.Fatal("expected error for missing method argument")
	}
}

// plainCaller implements only ContractCaller, to exercise the
// "backend does not support pending state" branch of Call.
type plainCaller struct{}

func (plainCaller) CodeAt(ctx context.Context, contract types.Address, blockNumber *uint256.Int) ([]byte, error) {
	return []byte{0x60}, nil
}

func (plainCaller) CallContract(ctx context.Context, call N42.CallMsg, blockNumber *uint256.Int) ([]byte, error) {
	return nil, nil
}
