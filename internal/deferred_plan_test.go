// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
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
