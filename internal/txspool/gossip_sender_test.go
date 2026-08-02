// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txspool

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// TestValidateSenderAcceptsTransactionWithoutDeclaredFrom is the regression for
// a silent mempool outage.
//
// Transactions arrive from gossip in the standard Ethereum encoding, which has
// no sender field -- the sender is whatever the signature recovers to, and that
// is the point: a peer must not be able to assert one. validateSender used to
// require a declared From and reject anything without it, so every gossiped
// transaction was dropped. It logged at Debug, the receive counter only
// advances after the pool accepts, and the publisher reports success whether or
// not anyone is listening, so nothing anywhere said the mempool had stopped
// propagating. Blocks kept flowing over the direct-push path, and only the
// submitting node's own turns carried transactions.
func TestValidateSenderAcceptsTransactionWithoutDeclaredFrom(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := uint256.NewInt(94)
	pool := &TxsPool{chainconfig: &params.ChainConfig{ChainID: chainID.ToBig()}}
	signer := transaction.LatestSignerForChainID(chainID.ToBig())
	to := types.Address{0x11, 0x22}

	tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Round-trip through the wire encoding: this is exactly the transaction the
	// gossip subscriber hands to the pool, From field and all (that is, none).
	raw, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	fromWire, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fromWire.From() != nil {
		t.Fatal("the wire encoding must not carry a sender; the signature decides it")
	}

	if !pool.validateSender(fromWire) {
		t.Fatal("a validly signed transaction from gossip was rejected")
	}

	want, err := transaction.Sender(signer, tx)
	if err != nil {
		t.Fatal(err)
	}
	got := fromWire.From()
	if got == nil {
		t.Fatal("validateSender accepted the transaction but left From unset")
	}
	if *got != want {
		t.Fatalf("From was filled in as %s, want the recovered sender %s", *got, want)
	}
}

// TestValidateSenderRejectsForgedFrom keeps the other half of the contract: a
// transaction that DOES declare a sender must be the one it claims. This is the
// check that a wire-supplied From must never be trusted on its own.
func TestValidateSenderRejectsForgedFrom(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := uint256.NewInt(94)
	pool := &TxsPool{chainconfig: &params.ChainConfig{ChainID: chainID.ToBig()}}
	signer := transaction.LatestSignerForChainID(chainID.ToBig())
	to := types.Address{0x33}

	tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     2,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}

	tx.SetFrom(types.Address{0xde, 0xad, 0xbe, 0xef})
	if pool.validateSender(tx) {
		t.Fatal("a transaction claiming a sender it did not sign for was accepted")
	}
}
