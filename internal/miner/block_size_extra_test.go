// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
)

func TestBundlePayloadSizeSumsTransactions(t *testing.T) {
	tx1 := testTransfer(0, 0)
	tx2 := testTransfer(1, 100)

	size1, err := txPayloadSize(tx1)
	if err != nil {
		t.Fatalf("txPayloadSize: %v", err)
	}
	size2, err := txPayloadSize(tx2)
	if err != nil {
		t.Fatalf("txPayloadSize: %v", err)
	}

	total, err := bundlePayloadSize([]*transaction.Transaction{tx1, tx2})
	if err != nil {
		t.Fatalf("bundlePayloadSize: %v", err)
	}
	if total != size1+size2 {
		t.Fatalf("expected bundle size %d, got %d", size1+size2, total)
	}
}

func TestBundlePayloadSizeEmpty(t *testing.T) {
	total, err := bundlePayloadSize(nil)
	if err != nil {
		t.Fatalf("bundlePayloadSize: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 for empty bundle, got %d", total)
	}
}

func TestBlockSizeLimiterRemaining(t *testing.T) {
	header := testMiningHeader(1)
	lim := newBlockSizeLimiter(header)

	before := lim.remaining()
	if before <= 0 {
		t.Fatalf("expected positive remaining budget, got %d", before)
	}

	tx := testTransfer(0, 0)
	size, decision, err := lim.admit(tx)
	if err != nil || decision != packAccept {
		t.Fatalf("expected tx to be admitted, got decision=%v err=%v", decision, err)
	}
	lim.add(size)

	after := lim.remaining()
	if after != before-size {
		t.Fatalf("expected remaining to shrink by %d, got before=%d after=%d", size, before, after)
	}
}

func TestBlockSizeLimiterRemainingNeverNegative(t *testing.T) {
	header := testMiningHeader(1)
	lim := newBlockSizeLimiter(header)
	// Force used past budget directly to exercise the clamp branch.
	lim.used = lim.budget + 1000
	if got := lim.remaining(); got != 0 {
		t.Fatalf("expected remaining clamped to 0, got %d", got)
	}
}
