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
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// fakeDeployBackend is a minimal DeployBackend for exercising WaitMined and
// WaitDeployed without a real client.
type fakeDeployBackend struct {
	receipt    *block.Receipt
	receiptErr error
	code       []byte
	codeErr    error
}

func (b *fakeDeployBackend) TransactionReceipt(ctx context.Context, txHash types.Hash) (*block.Receipt, error) {
	return b.receipt, b.receiptErr
}

func (b *fakeDeployBackend) CodeAt(ctx context.Context, account types.Address, blockNumber *big.Int) ([]byte, error) {
	return b.code, b.codeErr
}

func newLegacyTx(to *types.Address) *transaction.Transaction {
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		To:       to,
		From:     &types.Address{},
		Value:    uint256.NewInt(0),
		Gas:      21000,
		GasPrice: uint256.NewInt(1),
		Data:     nil,
	})
}

func TestWaitMinedImmediateSuccess(t *testing.T) {
	want := &block.Receipt{Status: block.ReceiptStatusSuccessful}
	backend := &fakeDeployBackend{receipt: want}

	tx := newLegacyTx(nil)
	got, err := WaitMined(context.Background(), backend, tx)
	if err != nil {
		t.Fatalf("WaitMined: %v", err)
	}
	if got != want {
		t.Fatalf("WaitMined returned %v, want %v", got, want)
	}
}

func TestWaitMinedContextCanceled(t *testing.T) {
	backend := &fakeDeployBackend{receiptErr: NotFound}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before WaitMined is even called

	tx := newLegacyTx(nil)
	_, err := WaitMined(ctx, backend, tx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitMined err = %v, want context.Canceled", err)
	}
}

func TestWaitDeployedRejectsNonCreationTx(t *testing.T) {
	to := types.Address{1, 2, 3}
	tx := newLegacyTx(&to)

	backend := &fakeDeployBackend{}
	if _, err := WaitDeployed(context.Background(), backend, tx); err == nil {
		t.Fatal("expected error for a non contract-creation transaction")
	}
}

func TestWaitDeployedSuccess(t *testing.T) {
	contractAddr := types.Address{9, 9, 9}
	backend := &fakeDeployBackend{
		receipt: &block.Receipt{
			Status:          block.ReceiptStatusSuccessful,
			ContractAddress: contractAddr,
		},
		code: []byte{0x60, 0x60}, // non-empty code
	}

	tx := newLegacyTx(nil)
	addr, err := WaitDeployed(context.Background(), backend, tx)
	if err != nil {
		t.Fatalf("WaitDeployed: %v", err)
	}
	if addr != contractAddr {
		t.Fatalf("WaitDeployed addr = %v, want %v", addr, contractAddr)
	}
}

func TestWaitDeployedZeroAddress(t *testing.T) {
	backend := &fakeDeployBackend{
		receipt: &block.Receipt{Status: block.ReceiptStatusSuccessful},
	}
	tx := newLegacyTx(nil)
	if _, err := WaitDeployed(context.Background(), backend, tx); err == nil {
		t.Fatal("expected error for zero contract address")
	}
}

func TestWaitDeployedNoCodeAfterDeploy(t *testing.T) {
	contractAddr := types.Address{1}
	backend := &fakeDeployBackend{
		receipt: &block.Receipt{
			Status:          block.ReceiptStatusSuccessful,
			ContractAddress: contractAddr,
		},
		code: nil, // empty code at the deployed address
	}
	tx := newLegacyTx(nil)
	_, err := WaitDeployed(context.Background(), backend, tx)
	if !errors.Is(err, ErrNoCodeAfterDeploy) {
		t.Fatalf("WaitDeployed err = %v, want ErrNoCodeAfterDeploy", err)
	}
}
