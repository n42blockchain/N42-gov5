// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"testing"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
)

// TxRootAt switches from the keccak MPT to the BLAKE3 binary root exactly
// at TxRootBlake3Time, and is TxRoot while the gate is unset.
func TestTxRootAtGate(t *testing.T) {
	prevEth, prevGate := UseEthereumTxRoot, TxRootBlake3Time
	defer func() { UseEthereumTxRoot, TxRootBlake3Time = prevEth, prevGate }()
	UseEthereumTxRoot = true
	txs := benchTxs(3000)
	mpt := hash.DeriveShaErigon(transaction.EthTransactions(txs))
	bin := hash.Blake3BinaryRoot(transaction.EthTransactions(txs))
	if mpt == bin {
		t.Fatal("the two roots should differ")
	}
	TxRootBlake3Time = 0
	if TxRootAt(txs, 1_800_000_000) != mpt {
		t.Fatal("gate unset: expected the MPT root")
	}
	TxRootBlake3Time = 1_800_000_000
	if TxRootAt(txs, 1_799_999_999) != mpt {
		t.Fatal("before the gate: expected the MPT root")
	}
	if TxRootAt(txs, 1_800_000_000) != bin {
		t.Fatal("at the gate: expected the BLAKE3 binary root")
	}
	if TxRootAt(nil, 1_800_000_000) != hash.EmptyBlake3Root {
		t.Fatal("empty list past the gate: expected the empty BLAKE3 root")
	}
	if TxRootAt(nil, 1) != TxRoot(nil) {
		t.Fatal("empty list before the gate: expected TxRoot's value")
	}
}
