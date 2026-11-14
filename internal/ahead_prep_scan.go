// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// N42_AHEAD_PREP_SCAN=1 (default off): a block's preparation (ahead_prep.go)
// also computes the two state-free scans its ahead run starts with, and the
// run adopts them instead of scanning the transactions again.
//
// An ahead run of a 326k-transaction block walked its transactions twice
// before its first wave (run 67, node0, full blocks): ParallelApplicable
// (touchesAny over the fee recipients) in executeParallelAhead, 4.8-6.1 ms
// mean -- the "unlogged" part of the run -- and canDeferParallelFeesStatic
// at the top of the run's setup (blockStartMs, 3.7-5.7 ms). Both read only
// the transactions, the header and the chain config, and the preparation,
// which runs while the block waits in the queue, already walks the same
// transactions.
//
// The scans depend on the transactions' senders. canDeferParallelFeesStatic
// takes a sender from the transaction (From, attached) or from its sender
// memo under the run's signer (a frame block's senders are memos, written by
// its sender job before the preparation starts); touchesAny looks at
// attached senders only. A scan is recorded only when every transaction has
// one or the other (the memo read without recovering), and only when
// touchesAny's answer is the same whether none or all of the memo-only
// senders get attached before the run (an attached sender is the true one,
// the memo's): the run's answer then equals the scan's whatever was attached
// meanwhile. The run adopts the scans only for the very transaction list
// scanned (the same objects, in order) under an equal signer; otherwise it
// scans as before.
var aheadPrepScanOn = parseOptIn(os.Getenv("N42_AHEAD_PREP_SCAN"))

// SetAheadPrepScanForTest switches N42_AHEAD_PREP_SCAN and returns the
// restore.
func SetAheadPrepScanForTest(on bool) func() {
	prev := aheadPrepScanOn
	aheadPrepScanOn = on
	return func() { aheadPrepScanOn = prev }
}

// aheadPrepScanHits counts the scans ahead runs adopted (process-wide).
var aheadPrepScanHits atomic.Uint64

// prepScan is a block's state-free scans, computed by its preparation.
type prepScan struct {
	txs    []*transaction.Transaction
	signer transaction.Signer
	// applicable: ParallelApplicable; deferStatic: canDeferParallelFeesStatic
	// with the run's recipients and precompiles.
	applicable, deferStatic bool
}

// prepStaticScan computes b's scans, or nil when a transaction has no sender
// attached yet.
func (p *StateProcessor) prepStaticScan(b *block.Block, hdr *block.Header, signer transaction.Signer) *prepScan {
	txs := b.Transactions()
	s := &prepScan{txs: txs, signer: signer}
	if len(txs) <= 4 || p.config.IsGlamsterdam(hdr.Time) {
		return s
	}
	recipients := deferredFeeRecipients(p.config, hdr)
	precompiles := vm2.ActivePrecompiles(p.config.RulesWithTimestamp(hdr.Number.Uint64(), hdr.Time))
	touches, deferOK, complete := scanTxsStatic(txs, recipients, precompiles, signer)
	if !complete {
		return nil
	}
	s.applicable = !touches
	s.deferStatic = deferOK
	return s
}

// scanTxsStatic is touchesAny and canDeferParallelFeesStatic (under signer)
// in one pass over txs. complete is false when a transaction has neither an
// attached sender nor a memo under signer, or when touchesAny's answer would
// change if the memo-only senders were attached; the results are then not
// those of the two functions.
func scanTxsStatic(txs []*transaction.Transaction, recipients, precompiles []types.Address, signer transaction.Signer) (touches, deferOK, complete bool) {
	in := func(a types.Address, set []types.Address) bool {
		for i := range set {
			if set[i] == a {
				return true
			}
		}
		return false
	}
	// hit: touchesAny over the attached senders; hitAll: over every
	// sender, memo-only ones included.
	var hit, hitAll, bad, missing atomic.Bool
	part := func(txs []*transaction.Transaction) {
		for _, tx := range txs {
			attached := tx.From()
			var from types.Address
			switch {
			case attached != nil:
				from = *attached
			case signer != nil && tx.HasSenderMemo(signer):
				// A memo hit: Sender does not recover.
				a, err := transaction.Sender(signer, tx)
				if err != nil {
					missing.Store(true)
					return
				}
				from = a
			default:
				missing.Store(true)
				return
			}
			to := tx.To()
			toHit := to != nil && in(*to, recipients)
			fromHit := in(from, recipients)
			if toHit || (attached != nil && fromHit) {
				hit.Store(true)
			}
			if toHit || fromHit {
				hitAll.Store(true)
				bad.Store(true)
				continue
			}
			if to == nil || len(tx.Data()) != 0 || len(tx.AuthList()) != 0 || in(*to, precompiles) {
				bad.Store(true)
			}
		}
	}
	workers := len(txs) / touchesAnyParallelMin
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		part(txs)
	} else {
		per := (len(txs) + workers - 1) / workers
		var wg sync.WaitGroup
		for lo := 0; lo < len(txs); lo += per {
			hi := min(lo+per, len(txs))
			wg.Add(1)
			go func(txs []*transaction.Transaction) {
				defer wg.Done()
				part(txs)
			}(txs[lo:hi])
		}
		wg.Wait()
	}
	if missing.Load() || hit.Load() != hitAll.Load() {
		return false, false, false
	}
	return hit.Load(), !bad.Load(), true
}

// scanFor returns the preparation's scans when they were computed for txs
// (the same transaction objects, in order), else nil.
func (pr *aheadPrep) scanFor(txs []*transaction.Transaction, signer transaction.Signer) *prepScan {
	if pr == nil || pr.scan == nil {
		return nil
	}
	s := pr.scan
	if s.signer == nil || signer == nil || !s.signer.Equal(signer) {
		return nil
	}
	if len(s.txs) != len(txs) {
		return nil
	}
	if len(txs) == 0 || &s.txs[0] == &txs[0] {
		return s
	}
	for i := range txs {
		if s.txs[i] != txs[i] {
			return nil
		}
	}
	return s
}
