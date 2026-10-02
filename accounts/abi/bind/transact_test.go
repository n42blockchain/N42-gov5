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
	"strings"
	"testing"

	"github.com/holiman/uint256"
	N42 "github.com/n42blockchain/N42"
	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

const testSetValueABI = `[{
	"constant": false,
	"inputs": [{"name": "v", "type": "uint256"}],
	"name": "setValue",
	"outputs": [],
	"type": "function"
}]`

// fakeContractTransactor is a minimal in-memory ContractTransactor for
// exercising BoundContract.Transact's gas/nonce/signer plumbing.
type fakeContractTransactor struct {
	header      *block.Header
	headerErr   error
	pendingCode []byte
	nonce       uint64
	gasPrice    *uint256.Int
	gasTipCap   *uint256.Int
	estimatedGas uint64
	sendErr     error
	sent        *transaction.Transaction
}

func (f *fakeContractTransactor) HeaderByNumber(ctx context.Context, number *uint256.Int) (*block.Header, error) {
	return f.header, f.headerErr
}
func (f *fakeContractTransactor) PendingCodeAt(ctx context.Context, account types.Address) ([]byte, error) {
	return f.pendingCode, nil
}
func (f *fakeContractTransactor) PendingNonceAt(ctx context.Context, account types.Address) (uint64, error) {
	return f.nonce, nil
}
func (f *fakeContractTransactor) SuggestGasPrice(ctx context.Context) (*uint256.Int, error) {
	return f.gasPrice, nil
}
func (f *fakeContractTransactor) SuggestGasTipCap(ctx context.Context) (*uint256.Int, error) {
	return f.gasTipCap, nil
}
func (f *fakeContractTransactor) EstimateGas(ctx context.Context, call N42.CallMsg) (uint64, error) {
	return f.estimatedGas, nil
}
func (f *fakeContractTransactor) SendTransaction(ctx context.Context, tx *transaction.Transaction) error {
	f.sent = tx
	return f.sendErr
}

func mustSetValueABI(t *testing.T) abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(testSetValueABI))
	if err != nil {
		t.Fatalf("abi.JSON: %v", err)
	}
	return parsed
}

func TestTransactRejectsConflictingGasFields(t *testing.T) {
	parsed := mustSetValueABI(t)
	c := NewBoundContract(types.Address{}, parsed, nil, &fakeContractTransactor{}, nil)

	opts := &TransactOpts{
		GasPrice:  uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
	}
	if _, err := c.Transact(opts, "setValue", uint256.NewInt(1).ToBig()); err == nil {
		t.Fatal("expected error for both gasPrice and EIP-1559 fields set")
	}
}

func TestTransactLegacyPathWithNoSend(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{
		pendingCode: []byte{0x60},
		nonce:       5,
		gasPrice:    uint256.NewInt(10),
	}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	signed := false
	opts := &TransactOpts{
		From:     types.Address{2},
		NoSend:   true,
		GasPrice: uint256.NewInt(5), // skip the HeaderByNumber lookup entirely
		Signer: func(addr types.Address, tx *transaction.Transaction) (*transaction.Transaction, error) {
			signed = true
			return tx, nil
		},
	}

	tx, err := c.Transact(opts, "setValue", uint256.NewInt(7).ToBig())
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if !signed {
		t.Fatal("expected Signer to be invoked")
	}
	if tx == nil {
		t.Fatal("expected a signed transaction to be returned")
	}
	if transactor.sent != nil {
		t.Fatal("NoSend should prevent SendTransaction from being called")
	}
}

func TestTransactMissingSigner(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{
		pendingCode: []byte{0x60},
		gasPrice:    uint256.NewInt(10),
	}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	opts := &TransactOpts{GasPrice: uint256.NewInt(5)}
	if _, err := c.Transact(opts, "setValue", uint256.NewInt(1).ToBig()); err == nil {
		t.Fatal("expected error for missing Signer")
	}
}

func TestTransactSendsWhenNotNoSend(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{
		pendingCode: []byte{0x60},
		gasPrice:    uint256.NewInt(10),
	}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	opts := &TransactOpts{
		GasPrice: uint256.NewInt(5),
		Signer: func(addr types.Address, tx *transaction.Transaction) (*transaction.Transaction, error) {
			return tx, nil
		},
	}
	if _, err := c.Transact(opts, "setValue", uint256.NewInt(1).ToBig()); err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if transactor.sent == nil {
		t.Fatal("expected SendTransaction to be invoked")
	}
}

func TestCreateLegacyTxRejectsDynamicFeeFields(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{pendingCode: []byte{0x60}}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	contract := types.Address{1}
	opts := &TransactOpts{GasFeeCap: uint256.NewInt(1)}
	if _, err := c.createLegacyTx(opts, &contract, nil); err == nil {
		t.Fatal("expected error when GasFeeCap is set for a legacy tx")
	}
}

func TestEstimateGasLimitNoCode(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{pendingCode: nil}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	contract := types.Address{1}
	opts := &TransactOpts{}
	_, err := c.estimateGasLimit(opts, &contract, nil, uint256.NewInt(0), uint256.NewInt(0), uint256.NewInt(0), uint256.NewInt(0))
	if err != ErrNoCode {
		t.Fatalf("estimateGasLimit err = %v, want ErrNoCode", err)
	}
}

func TestGetNonceExplicit(t *testing.T) {
	parsed := mustSetValueABI(t)
	transactor := &fakeContractTransactor{nonce: 99}
	c := NewBoundContract(types.Address{1}, parsed, nil, transactor, nil)

	explicit := uint64(42)
	got, err := c.getNonce(&TransactOpts{Nonce: &explicit})
	if err != nil || got != explicit {
		t.Fatalf("getNonce(explicit) = %d, %v, want %d", got, err, explicit)
	}

	got2, err := c.getNonce(&TransactOpts{})
	if err != nil || got2 != 99 {
		t.Fatalf("getNonce(pending) = %d, %v, want 99", got2, err)
	}
}
