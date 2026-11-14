// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Parallel tx results, result pooling and deferred-fee eligibility.

package internal

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// parallelWorkers is the Block-STM worker count: N42_PARALLEL_WORKERS, default
// 32. One read transaction per worker is a different resource than one
// goroutine per worker (94775a0a), so this is not NumCPU (256 here).
func parallelWorkers() int {
	if v := os.Getenv("N42_PARALLEL_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 32
}

// parallelTxResult stores per-transaction execution output from parallel execution.
type parallelTxResult struct {
	receipt *block.Receipt
	gasUsed uint64
	err     error
	logs    []*block.Log
	fees    []deferredFee
	// feeBuf holds fees in place for the usual one or two credits, so a
	// transaction's fees cost no allocation.
	feeBuf [2]deferredFee
}

// setFees stores a copy of fees in the slot.
func (r *parallelTxResult) setFees(fees []deferredFee) {
	switch {
	case len(fees) == 0:
		r.fees = nil
	case len(fees) <= len(r.feeBuf):
		n := copy(r.feeBuf[:], fees)
		r.fees = r.feeBuf[:n:n]
	default:
		r.fees = append([]deferredFee(nil), fees...)
	}
}

// txResultsFree keeps a block's worth of parallelTxResult slots across blocks.
// A fresh 163k-element slice per block is ~10 MB the runtime hands back to
// the OS under the memory limit and page-faults in again on the next block;
// with the executor arena in place that fault was the whole 60 ms "executorMs"
// of a follower's import (round 35zza). Two slots: import and builder.
var txResultsFree struct {
	mu   sync.Mutex
	list [][]parallelTxResult
}

func takeTxResults(n int) []parallelTxResult {
	txResultsFree.mu.Lock()
	defer txResultsFree.mu.Unlock()
	for i := len(txResultsFree.list) - 1; i >= 0; i-- {
		if r := txResultsFree.list[i]; cap(r) >= n {
			txResultsFree.list = append(txResultsFree.list[:i], txResultsFree.list[i+1:]...)
			// Zero up to its capacity already: giveTxResults cleared it,
			// and what lies past its last length was never used since.
			return r[:n]
		}
	}
	return make([]parallelTxResult, n)
}

func giveTxResults(r []parallelTxResult) {
	clear(r) // drop the receipt and log pointers now; takeTxResults relies on it
	txResultsFree.mu.Lock()
	defer txResultsFree.mu.Unlock()
	if len(txResultsFree.list) < 2 {
		txResultsFree.list = append(txResultsFree.list, r[:0])
		return
	}
	// Full: replace the smallest kept slice if this one is larger, so two
	// small slices from a leg's ramp cannot hold the list for the run (the
	// same trap as the executor arena's free list, round 35zzh).
	smallest := 0
	for i, k := range txResultsFree.list {
		if cap(k) < cap(txResultsFree.list[smallest]) {
			smallest = i
		}
	}
	if cap(txResultsFree.list[smallest]) < cap(r) {
		txResultsFree.list[smallest] = r[:0]
	}
}

// deferredFee is a block-producer credit a transaction diverted to its fee
// sink; ProcessParallel sums them and credits the state once per recipient.
type deferredFee struct {
	recipient types.Address
	amount    uint256.Int
}

// deferredFeeRecipients lists the accounts whose credits ProcessParallel
// defers to the end of the block: the priority-fee recipient and, when the
// chain collects the base fee, the collector.
func deferredFeeRecipients(cfg *params.ChainConfig, header *block.Header) []types.Address {
	policy := newTransitionChainPolicy(cfg)
	recipients := []types.Address{policy.priorityFeeRecipient(header.Coinbase)}
	if cfg != nil && cfg.Eip1559FeeCollector != nil {
		if c := *cfg.Eip1559FeeCollector; c != recipients[0] {
			recipients = append(recipients, c)
		}
	}
	return recipients
}

// touchesAny reports whether any transaction sends from or to one of the
// addresses. Such a transaction would observe a coinbase balance that the
// deferred credit has not yet reached, so the block must run sequentially.
func touchesAny(txs []*transaction.Transaction, addrs []types.Address) bool {
	if len(txs) >= touchesAnyParallelMin {
		return touchesAnyParallel(txs, addrs)
	}
	return touchesAnyRange(txs, addrs)
}

// touchesAnyParallelMin is the list size from which touchesAny splits the
// scan across goroutines (P10: the serial scan of a 163k block was ~6.6 ms
// on the fleet, twice per ahead run, all cache misses on the transactions).
const touchesAnyParallelMin = 8192

func touchesAnyParallel(txs []*transaction.Transaction, addrs []types.Address) bool {
	workers := len(txs) / touchesAnyParallelMin
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		return touchesAnyRange(txs, addrs)
	}
	per := (len(txs) + workers - 1) / workers
	var hit atomic.Bool
	var wg sync.WaitGroup
	for lo := 0; lo < len(txs); lo += per {
		hi := min(lo+per, len(txs))
		wg.Add(1)
		go func(part []*transaction.Transaction) {
			defer wg.Done()
			if touchesAnyRange(part, addrs) {
				hit.Store(true)
			}
		}(txs[lo:hi])
	}
	wg.Wait()
	return hit.Load()
}

func touchesAnyRange(txs []*transaction.Transaction, addrs []types.Address) bool {
	for _, tx := range txs {
		for _, a := range addrs {
			if from := tx.From(); from != nil && *from == a {
				return true
			}
			if to := tx.To(); to != nil && *to == a {
				return true
			}
		}
	}
	return false
}

// canDeferParallelFees runs after sender recovery and block-start processing.
// One executable transaction makes deferral unsafe for the entire block.
func canDeferParallelFees(txs []*transaction.Transaction, recipients []types.Address, ibs *state.IntraBlockState, precompiles []types.Address) (bool, error) {
	if !canDeferParallelFeesStatic(txs, recipients, precompiles, nil) {
		return false, nil
	}
	seen := make(map[types.Address]bool)
	for _, tx := range txs {
		to := tx.To()
		if seen[*to] {
			continue
		}
		seen[*to] = true
		if len(ibs.GetCode(*to)) != 0 {
			return false, ibs.Error()
		}
		if err := ibs.Error(); err != nil {
			return false, fmt.Errorf("parallel fee eligibility: %w", err)
		}
	}
	return true, nil
}

// canDeferParallelFeesStatic is canDeferParallelFees without the state
// reads: every transaction a value transfer (no creation, no calldata, no
// authorization, no precompile) by a known sender, none from or to a fee
// recipient. A sender is known when declared (From) or recovered under
// signer (memoised by the run's sender recovery). Whether a recipient carries code is
// left to the caller: the Block-STM run checks it on its workers
// (runParallelMode, feeCodeSeen), so a block of plain transfers neither
// reads its recipients serially nor materialises them in the block's state
// before the wave. Large lists are scanned across goroutines, as touchesAny.
func canDeferParallelFeesStatic(txs []*transaction.Transaction, recipients []types.Address, precompiles []types.Address, signer transaction.Signer) bool {
	check := func(part []*transaction.Transaction) bool {
		for _, tx := range part {
			to := tx.To()
			if to == nil || len(tx.Data()) != 0 || len(tx.AuthList()) != 0 {
				return false
			}
			from := tx.From()
			if from == nil && signer != nil {
				// The run's sender recovery memoised it: an atomic load.
				if a, err := transaction.Sender(signer, tx); err == nil {
					from = &a
				}
			}
			if from == nil {
				return false
			}
			for _, address := range precompiles {
				if *to == address {
					return false
				}
			}
			for _, a := range recipients {
				if *from == a || *to == a {
					return false
				}
			}
		}
		return true
	}
	workers := len(txs) / touchesAnyParallelMin
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		return check(txs)
	}
	per := (len(txs) + workers - 1) / workers
	var fail atomic.Bool
	var wg sync.WaitGroup
	for lo := 0; lo < len(txs); lo += per {
		hi := min(lo+per, len(txs))
		wg.Add(1)
		go func(part []*transaction.Transaction) {
			defer wg.Done()
			if !check(part) {
				fail.Store(true)
			}
		}(txs[lo:hi])
	}
	wg.Wait()
	return !fail.Load()
}
