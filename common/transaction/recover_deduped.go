// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S59 (docs/QS_BLOCK_TIME_BUDGET.md 6fa): a single-recovery guarantee and a
// bounded, dedicated worker pool for the hint feed and RPC ingest paths --
// the two sites that recover a transaction's sender ahead of the pool/import
// pipeline, and the two the task scopes this fix to. senderCache already
// prevents a LATER, sequential caller from re-recovering a hash; it does
// NOT stop two callers that both miss the cache at the same instant from
// both paying for a real ECDSA recovery. RecoverSenderDeduped closes that.

package transaction

import (
	"encoding/binary"
	"os"
	"strconv"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// recoveryLockStripes bounds memory for the same-hash dedup below: a
// collision between two DIFFERENT hashes only costs unnecessary
// serialization, never a wrong answer (each caller re-checks the cache after
// acquiring the stripe).
const recoveryLockStripes = 1024

var recoveryLocks [recoveryLockStripes]sync.Mutex

func recoveryStripe(hash types.Hash) *sync.Mutex {
	idx := binary.LittleEndian.Uint64(hash[0:8]) % recoveryLockStripes
	return &recoveryLocks[idx]
}

// RecoverSenderDeduped recovers tx's sender under signer, guaranteeing at
// most one real ECDSA recovery per (hash, signer) even under concurrent
// callers: a striped lock serializes same-hash callers, and every caller
// re-checks the cache after acquiring its stripe, so a caller that loses the
// race sees the winner's cached result instead of repeating the curve math.
// Different hashes use different stripes (1024-way) and proceed in parallel.
func RecoverSenderDeduped(signer Signer, tx *Transaction) (types.Address, error) {
	if addr, ok := CachedSender(signer, tx); ok {
		return addr, nil
	}
	mu := recoveryStripe(tx.Hash())
	mu.Lock()
	defer mu.Unlock()
	if addr, ok := CachedSender(signer, tx); ok {
		return addr, nil // recovered by another goroutine while we waited
	}
	return Sender(signer, tx) // real recovery; Sender caches it for everyone
}

var (
	hintRecoveryPoolOnce sync.Once
	hintRecoveryJobs     chan func()
	hintRecoveryN        int
)

// HintRecoveryPool returns the configured worker count (0 = off, today's
// behaviour) and the shared job channel (nil when off), parsed and started
// once from N42_HINT_RECOVERY_POOL. Its own goroutines are dedicated to
// sender recovery for the hint feed and RPC ingest paths ONLY -- never
// submitted to, or drawn from, the block executor's own parallel-processing
// pool (internal/parallel_processor.go). They still share the process's
// GOMAXPROCS-wide OS-thread budget with everything else, as every Go
// goroutine does; what this bounds is how MANY of them can be runnable at
// once, in place of today's unbounded (RPC, one recovery per connection) or
// large-but-fixed (`EnableHintOnly`'s own workers, historically ~75% of
// GOMAXPROCS via senderRecoveryFanout's own sizing logic) concurrency.
func HintRecoveryPool() (int, chan func()) {
	hintRecoveryPoolOnce.Do(func() {
		v := os.Getenv("N42_HINT_RECOVERY_POOL")
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return
		}
		hintRecoveryN = n
		hintRecoveryJobs = make(chan func(), n*4)
		for i := 0; i < n; i++ {
			go func() {
				for job := range hintRecoveryJobs {
					job()
				}
			}()
		}
	})
	return hintRecoveryN, hintRecoveryJobs
}

// RecoverOnPool runs recover (typically a single RecoverSenderDeduped call)
// either inline (pool off, today's behaviour) or dispatched to the shared
// hint-recovery pool and awaited (pool on). The caller blocks either way --
// this only changes WHICH goroutine does the CPU work, not the call's own
// synchronous contract.
func RecoverOnPool(recover func()) {
	n, jobs := HintRecoveryPool()
	if n < 1 || jobs == nil {
		recover()
		return
	}
	done := make(chan struct{})
	jobs <- func() {
		recover()
		close(done)
	}
	<-done
}
