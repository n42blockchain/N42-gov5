// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// A block's sender job started in the background and joined by the import
// leaves every transaction with the sender plain recovery derives, and the
// join returns only once all of them are done.
func TestSenderJobJoinRecoversAll(t *testing.T) {
	const n = 1000
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs, want := buildSignedTxs(t, n)
	j := &senderJob{signer: signer, txs: txs, start: time.Now()}
	done := make(chan struct{})
	j.spawn(1, func() { close(done) })
	j.join(4)
	select {
	case <-done:
	default:
		t.Fatal("join returned before the background worker finished")
	}
	for i, tx := range txs {
		got, ok := transaction.CachedSender(signer, tx)
		if !ok {
			t.Fatalf("tx %d: not recovered when join returned", i)
		}
		if got != want[i] {
			t.Fatalf("tx %d: sender %s, want %s", i, got.Hex(), want[i].Hex())
		}
	}
	if next := j.next.Load(); next < n {
		t.Fatalf("cursor at %d of %d", next, n)
	}
}

// The import joins a job only for its own transactions under its own rules;
// anything else falls back to plain recovery, and a taken job is gone.
func TestTakeSenderJobMatchesOnlyItsBlock(t *testing.T) {
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs, _ := buildSignedTxs(t, 16)
	other, _ := buildSignedTxs(t, 16)
	bc := &BlockChain{}
	h := types.Hash{0x01}
	put := func() {
		bc.senderJobs.mu.Lock()
		if bc.senderJobs.m == nil {
			bc.senderJobs.m = make(map[types.Hash]*senderJob)
		}
		bc.senderJobs.m[h] = &senderJob{signer: signer, txs: txs}
		bc.senderJobs.mu.Unlock()
	}
	put()
	if bc.takeSenderJob(h, signer, other) != nil {
		t.Fatal("joined a job over other transactions")
	}
	put()
	if bc.takeSenderJob(h, transaction.NewLondonSigner(big.NewInt(95)), txs) != nil {
		t.Fatal("joined a job under another signer")
	}
	put()
	if bc.takeSenderJob(h, signer, txs) == nil {
		t.Fatal("did not join its own job")
	}
	if bc.takeSenderJob(h, signer, txs) != nil {
		t.Fatal("a taken job was joined twice")
	}
	// No job: plain recovery, also on a nil chain.
	var nilBC *BlockChain
	nilBC.recoverBlockSendersFor(h, signer, other, nil)
	for i, tx := range other {
		if _, ok := transaction.CachedSender(signer, tx); !ok {
			t.Fatalf("tx %d not recovered by the fallback", i)
		}
	}
}
