// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Thread-safe gas pool for parallel EVM execution. Wraps a uint64 in
// atomic operations; AddGas / SubGas are lock-free. Used by the
// Block-STM worker path alongside (not replacing) the existing
// sequential GasPool.
//
// Design note: in a true Block-STM pipeline, gas pool mutation should
// be DEFERRED to commit phase so speculative executions don't bleed
// gas from each other. This atomic version is for PoC + the simpler
// "check at commit" execution model; a speculative executor on top
// would record the gas delta per tx and apply to the AtomicGasPool
// only when the tx commits.

package common

import (
	"math"
	"sync/atomic"
)

// AtomicGasPool is a thread-safe gas budget. Zero value is a pool
// with zero gas available (same semantics as *GasPool).
type AtomicGasPool struct {
	remaining atomic.Uint64
}

// NewAtomicGasPool returns a pool with `limit` gas available.
func NewAtomicGasPool(limit uint64) *AtomicGasPool {
	p := &AtomicGasPool{}
	p.remaining.Store(limit)
	return p
}

// AddGas adds gas to the pool. Panics on uint64 overflow (matches the
// sequential GasPool's panic).
func (p *AtomicGasPool) AddGas(amount uint64) *AtomicGasPool {
	for {
		cur := p.remaining.Load()
		if cur > math.MaxUint64-amount {
			panic("atomic gas pool pushed above uint64")
		}
		if p.remaining.CompareAndSwap(cur, cur+amount) {
			return p
		}
	}
}

// SubGas deducts the given amount from the pool iff enough gas is
// available. Returns ErrGasLimitReached if the pool is exhausted.
// The CAS loop retries on contention; for typical mainnet-block gas
// accounting (~200 SubGas calls/block) contention cost is negligible.
func (p *AtomicGasPool) SubGas(amount uint64) error {
	for {
		cur := p.remaining.Load()
		if cur < amount {
			return ErrGasLimitReached
		}
		if p.remaining.CompareAndSwap(cur, cur-amount) {
			return nil
		}
	}
}

// Gas returns the current remaining gas.
func (p *AtomicGasPool) Gas() uint64 {
	return p.remaining.Load()
}

// SetGas unconditionally sets the remaining gas (used for resetting
// at block boundaries).
func (p *AtomicGasPool) SetGas(v uint64) {
	p.remaining.Store(v)
}
