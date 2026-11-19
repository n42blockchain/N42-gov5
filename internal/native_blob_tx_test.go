// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

func nativeBlobTestTx(t *testing.T, chainID int64, hashes []types.Hash) *transaction.Transaction {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID)),
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(100),
		Gas:        100_000,
		To:         to,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: hashes,
	}), transaction.LatestSignerForChainID(big.NewInt(chainID)), key)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	tx.SetFrom(crypto.PubkeyToAddress(key.PublicKey))
	return tx
}

// A blob (type-3) transaction is invalid on an N42 native chain at every
// fork (decision D4 of the Rust client's PROTOCOL_CONFORMANCE_2026_11.md):
// execution refuses it, with or without blob hashes. An Ethereum chain keeps
// the EIP-4844 rules.
func TestApplyTransactionRefusesBlobTxOnNativeChain(t *testing.T) {
	kzg := types.Hash{transaction.VersionedHashVersionKZG}
	for _, tc := range []struct {
		name   string
		config *params.ChainConfig
		hashes []types.Hash
		native bool
	}{
		{"ethereum", testStateTransitionChainConfig(), []types.Hash{kzg}, false},
		{"native", nativeShapeTestConfig(), []types.Hash{kzg}, true},
		{"native without hashes", nativeShapeTestConfig(), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := nativeBlobTestTx(t, 1, tc.hashes)
			// The builder is unused: the signed transaction is handed in whole.
			err := applyShapeTestTx(t, tc.config, func() transaction.TxData { return &transaction.LegacyTx{} }, func(*transaction.Transaction) *transaction.Transaction { return tx })
			if got := errors.Is(err, ErrBlobTxOnNativeChain); got != tc.native {
				t.Fatalf("err = %v, refused as native %v, want %v", err, got, tc.native)
			}
			if tc.native && !errors.Is(err, ErrTxTypeNotSupported) {
				t.Fatalf("err = %v, want it to wrap ErrTxTypeNotSupported", err)
			}
		})
	}
}

// Under deferred execution a blob transaction is not includable, by its type
// as well as by its hashes: the Rust client's check refuses the same set.
func TestDeferredTxPlanRefusesBlobTx(t *testing.T) {
	cfg := deferredTestConfig()
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 30_000_000, BaseFee: uint256.NewInt(1)}
	for _, hashes := range [][]types.Hash{nil, {{transaction.VersionedHashVersionKZG}}} {
		tx := nativeBlobTestTx(t, 94, hashes)
		_, err := deferredTxPlan(cfg, hdr, []*transaction.Transaction{tx})
		if err == nil || !strings.Contains(err.Error(), "blob transactions are not includable under deferred execution") {
			t.Fatalf("%d hashes: got %v", len(hashes), err)
		}
	}
}
