// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// deferredTestConfig enables every block-numbered fork at genesis (London
// included) on chain 94, the signer buildSignedTxs uses.
func deferredTestConfig() *params.ChainConfig {
	cfg := &params.ChainConfig{ChainID: big.NewInt(94)}
	v := reflect.ValueOf(cfg).Elem()
	bigT := reflect.TypeOf((*big.Int)(nil))
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.Type == bigT && strings.HasSuffix(f.Name, "Block") && f.Name != "DAOForkBlock" {
			v.Field(i).Set(reflect.ValueOf(big.NewInt(0)))
		}
	}
	return cfg
}

// The plan recovers the senders of wire-decoded transactions (From nil)
// without writing From -- the import writes it, possibly concurrently -- and
// groups them in block order.
func TestDeferredTxPlanRecoversWithoutFrom(t *testing.T) {
	txs, want := buildSignedTxs(t, 3000)
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 4000, BaseFee: uint256.NewInt(1_000_000_000)}
	plan, err := deferredTxPlan(deferredTestConfig(), hdr, txs)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != len(txs) {
		t.Fatalf("plan has %d senders, want %d", len(plan), len(txs))
	}
	for i, st := range plan {
		if st.addr != want[i] || len(st.txs) != 1 || st.txs[0] != txs[i] {
			t.Fatalf("sender %d: %x with %d txs, want %x", i, st.addr, len(st.txs), want[i])
		}
	}
	for i, tx := range txs {
		if tx.From() != nil {
			t.Fatalf("tx %d: the plan wrote From", i)
		}
	}
}

func TestDeferredTxPlanStatelessRules(t *testing.T) {
	cfg := deferredTestConfig()
	txs, _ := buildSignedTxs(t, 3000)

	// A base fee above every fee cap.
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 4000, BaseFee: uint256.NewInt(2_000_000_000)}
	if _, err := deferredTxPlan(cfg, hdr, txs); !errors.Is(err, ErrFeeCapTooLow) {
		t.Fatalf("fee cap below the base fee: got %v", err)
	}
	// The header's gas limit.
	hdr = &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 10, BaseFee: uint256.NewInt(1)}
	if _, err := deferredTxPlan(cfg, hdr, txs); err == nil || !strings.Contains(err.Error(), "gas limit") {
		t.Fatalf("block gas limit: got %v", err)
	}
	// Two unrecoverable signatures: the lowest index is reported, whatever
	// goroutine reached its own first.
	bad := append([]*transaction.Transaction(nil), txs...)
	bad[2800] = unrecoverableTx(t, 0)
	bad[100] = unrecoverableTx(t, 1)
	hdr = &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 4000, BaseFee: uint256.NewInt(1)}
	if _, err := deferredTxPlan(cfg, hdr, bad); err == nil || !strings.Contains(err.Error(), "tx 100 sender") {
		t.Fatalf("lowest failing index: got %v", err)
	}
}

// signTo signs one transaction from key to `to` with the given nonce and value.
func signTo(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to types.Address, value uint64) *transaction.Transaction {
	t.Helper()
	chainID, _ := uint256.FromBig(senderRecoveryChainID)
	inner := &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(value),
	}
	signed, err := transaction.SignNewTx(key, transaction.NewLondonSigner(senderRecoveryChainID), inner)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// A block that pays an account and then spends from it is includable: the
// payment lands before the spend when the block executes. The plan records
// what the block pays a sender BEFORE its first transaction, so the balance
// check can add it (35zzx aborted a round on exactly this shape -- the
// generators fund a sender and it submits in the same block).
func TestDeferredTxPlanCreditsEarlierPaymentsInTheBlock(t *testing.T) {
	payerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	spenderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	spender := crypto.PubkeyToAddress(spenderKey.PublicKey)
	sink := types.Address{0xCC}

	fund := signTo(t, payerKey, 0, spender, 5_000_000)
	spend := signTo(t, spenderKey, 0, sink, 1)
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 10, BaseFee: uint256.NewInt(1_000_000_000)}

	plan, err := deferredTxPlan(deferredTestConfig(), hdr, []*transaction.Transaction{fund, spend})
	if err != nil {
		t.Fatal(err)
	}
	var got *deferredSender
	for _, st := range plan {
		if st.addr == spender {
			got = st
		}
	}
	if got == nil {
		t.Fatal("the spender is not in the plan")
	}
	if got.credit.Uint64() != 5_000_000 {
		t.Fatalf("credit = %s, want 5000000", got.credit.String())
	}
}

// The other order earns nothing: a payment that lands AFTER the sender's own
// transaction cannot pay for it.
func TestDeferredTxPlanIgnoresLaterPayments(t *testing.T) {
	payerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	spenderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	spender := crypto.PubkeyToAddress(spenderKey.PublicKey)
	sink := types.Address{0xCD}

	spend := signTo(t, spenderKey, 0, sink, 1)
	fund := signTo(t, payerKey, 0, spender, 5_000_000)
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 21000 * 10, BaseFee: uint256.NewInt(1_000_000_000)}

	plan, err := deferredTxPlan(deferredTestConfig(), hdr, []*transaction.Transaction{spend, fund})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range plan {
		if st.addr == spender && !st.credit.IsZero() {
			t.Fatalf("a later payment was credited: %s", st.credit.String())
		}
	}
}
