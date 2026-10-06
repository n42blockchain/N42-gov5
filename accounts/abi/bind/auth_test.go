// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bind

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func newUnsignedTx(t *testing.T) *transaction.Transaction {
	t.Helper()
	to := types.Address{0x01}
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		To:       &to,
		From:     &types.Address{},
		Value:    uint256.NewInt(0),
		Gas:      21000,
		GasPrice: uint256.NewInt(1),
		Data:     nil,
	})
}

func TestNewKeyedTransactor(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	opts := NewKeyedTransactor(key)
	keyAddr := crypto.PubkeyToAddress(key.PublicKey)

	if opts.From != keyAddr {
		t.Fatalf("opts.From = %v, want %v", opts.From, keyAddr)
	}

	tx := newUnsignedTx(t)
	signed, err := opts.Signer(keyAddr, tx)
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	if signed == nil {
		t.Fatal("expected a signed transaction")
	}

	// Signing with the wrong "from" address must be rejected.
	wrongAddr := types.Address{0xEE}
	if _, err := opts.Signer(wrongAddr, tx); err != ErrNotAuthorized {
		t.Fatalf("Signer(wrong addr) err = %v, want ErrNotAuthorized", err)
	}
}

func TestNewKeyedTransactorWithChainID(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if _, err := NewKeyedTransactorWithChainID(key, nil); err != ErrNoChainID {
		t.Fatalf("NewKeyedTransactorWithChainID(nil chainID) err = %v, want ErrNoChainID", err)
	}

	chainID := big.NewInt(1337)
	opts, err := NewKeyedTransactorWithChainID(key, chainID)
	if err != nil {
		t.Fatalf("NewKeyedTransactorWithChainID: %v", err)
	}
	keyAddr := crypto.PubkeyToAddress(key.PublicKey)
	if opts.From != keyAddr {
		t.Fatalf("opts.From = %v, want %v", opts.From, keyAddr)
	}

	tx := newUnsignedTx(t)
	if _, err := opts.Signer(keyAddr, tx); err != nil {
		t.Fatalf("Signer: %v", err)
	}
	wrongAddr := types.Address{0xEE}
	if _, err := opts.Signer(wrongAddr, tx); err != ErrNotAuthorized {
		t.Fatalf("Signer(wrong addr) err = %v, want ErrNotAuthorized", err)
	}
}
