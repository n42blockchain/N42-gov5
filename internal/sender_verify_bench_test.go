// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cost of the import-time sender-verification gate (verifyBlockSenders).
//
// The gate is the price of closing the forged-sender hole: before it, a
// wire block whose transactions carry `From` did ZERO signature work at
// import. Two effects decide whether that price is acceptable:
//
//   - the process-wide senderCache: a transaction the pool already saw
//     (gossiped ahead of the block — the normal case on a saturated
//     fleet) verifies as a map lookup, no curve math;
//   - the worker fan-out: cold verifications parallelize across
//     the sender-recovery fan-out.
//
// Run: go test -tags nosqlite,noboltdb -run XXX -bench BenchmarkVerifyBlockSenders ./internal/

package internal

import (
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
)

// benchBlock builds n signed txs carrying their (honest) wire From, as a
// block arriving over the wire does after proto decode.
func benchBlock(b *testing.B, n int) ([]*transaction.Transaction, transaction.Signer) {
	b.Helper()
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs, want := buildSignedTxs(b, n)
	for i, tx := range txs {
		tx.SetFrom(want[i])
	}
	return txs, signer
}

func BenchmarkVerifyBlockSendersCold(b *testing.B) {
	// Worst case: nothing in the senderCache — every transaction pays a
	// full secp256k1 recovery. This is what a node sees for a block whose
	// transactions it never received via gossip.
	for _, n := range []int{182, 1000, 5000} {
		b.Run(fmt.Sprintf("txs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Fresh keys every iteration → guaranteed cache misses.
				txs, signer := benchBlock(b, n)
				b.StartTimer()
				if err := verifyBlockSenders(signer, txs); err != nil {
					b.Fatalf("unexpected verification failure: %v", err)
				}
			}
		})
	}
}

func BenchmarkVerifyBlockSendersWarm(b *testing.B) {
	// Realistic case on a saturated fleet: the pool already recovered
	// these senders when the transactions were gossiped, so the
	// process-wide cache serves them.
	for _, n := range []int{182, 1000, 5000} {
		b.Run(fmt.Sprintf("txs=%d", n), func(b *testing.B) {
			txs, signer := benchBlock(b, n)
			// Warm the cache once; the measured loop re-verifies.
			if err := verifyBlockSenders(signer, txs); err != nil {
				b.Fatalf("warmup failed: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := verifyBlockSenders(signer, txs); err != nil {
					b.Fatalf("unexpected verification failure: %v", err)
				}
			}
		})
	}
}

// BenchmarkVerifyBlockSendersSerial is the same cold work with the
// fan-out disabled, to show what the concurrency buys.
func BenchmarkVerifyBlockSendersSerial(b *testing.B) {
	saved := senderRecoveryWorkerOverride
	senderRecoveryWorkerOverride = 1
	defer func() { senderRecoveryWorkerOverride = saved }()

	for _, n := range []int{182, 1000} {
		b.Run(fmt.Sprintf("txs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				txs, signer := benchBlock(b, n)
				b.StartTimer()
				if err := verifyBlockSenders(signer, txs); err != nil {
					b.Fatalf("unexpected verification failure: %v", err)
				}
			}
		})
	}
}
