// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Escrow is the in-memory settlement ledger for slice 1: every submitted
// task locks MaxPrice; acceptance pays workers out of the locked amount and
// refunds the remainder; failure/expiry refunds everything. The conservation
// invariant (locked == Σpaid + Σrefunded once a task is settled) is what
// TestEscrowConservation pins down. On-chain settlement replaces this ledger
// in a later slice; the interface is deliberately minimal to keep that swap
// contained.

package scheduler

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Escrow tracks locked funds per task and cumulative payouts per worker.
type Escrow struct {
	mu       sync.Mutex
	locked   map[types.Hash]uint64    // task → still-locked remainder
	paid     map[types.Address]uint64 // worker → cumulative payouts
	refunded map[types.Hash]uint64    // task → refunded total
}

// NewEscrow creates an empty ledger.
func NewEscrow() *Escrow {
	return &Escrow{
		locked:   make(map[types.Hash]uint64),
		paid:     make(map[types.Address]uint64),
		refunded: make(map[types.Hash]uint64),
	}
}

// Lock reserves amount for a task. Called exactly once per task at submit.
func (e *Escrow) Lock(task types.Hash, amount uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locked[task] += amount
}

// Pay transfers amount from the task's locked funds to a worker.
func (e *Escrow) Pay(task types.Hash, worker types.Address, amount uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locked[task] < amount {
		return fmt.Errorf("escrow: task %s has %d locked, cannot pay %d",
			task.Hex()[:10], e.locked[task], amount)
	}
	e.locked[task] -= amount
	e.paid[worker] += amount
	return nil
}

// RefundRemainder returns whatever is still locked for the task to the
// submitter and zeroes the lock. Idempotent.
func (e *Escrow) RefundRemainder(task types.Hash) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rem := e.locked[task]; rem > 0 {
		e.refunded[task] += rem
		e.locked[task] = 0
	}
}

// PaidTo returns the cumulative amount paid to a worker.
func (e *Escrow) PaidTo(worker types.Address) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paid[worker]
}

// Refunded returns the total refunded for a task.
func (e *Escrow) Refunded(task types.Hash) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refunded[task]
}

// Locked returns the still-locked amount for a task.
func (e *Escrow) Locked(task types.Hash) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.locked[task]
}
