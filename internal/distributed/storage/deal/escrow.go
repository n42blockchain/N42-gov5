// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Escrow is the in-memory streaming settlement ledger for storage deals: a
// deal locks size×price×r×epochs at submission; each epoch a provider passes
// its challenge it earns size×price (Byte·Epoch); on completion or failure
// the remainder refunds to the submitter. The conservation invariant
// (locked == Σpaid + Σrefunded once settled) is what TestDealHappyPath and
// the concurrent test pin down. On-chain settlement replaces this later; the
// surface is deliberately small.

package deal

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Escrow tracks locked funds per deal and cumulative payouts per provider.
// paidByDeal additionally attributes payouts to the paying deal so per-deal
// conservation is checkable even when a provider earns across many deals.
type Escrow struct {
	mu         sync.Mutex
	locked     map[types.Hash]uint64
	paid       map[types.Address]uint64
	paidByDeal map[types.Hash]uint64
	refunded   map[types.Hash]uint64
}

// NewEscrow creates an empty ledger.
func NewEscrow() *Escrow {
	return &Escrow{
		locked:     make(map[types.Hash]uint64),
		paid:       make(map[types.Address]uint64),
		paidByDeal: make(map[types.Hash]uint64),
		refunded:   make(map[types.Hash]uint64),
	}
}

// Lock reserves amount for a deal (once, at submit).
func (e *Escrow) Lock(deal types.Hash, amount uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locked[deal] += amount
}

// Pay transfers amount from the deal's locked funds to a provider.
func (e *Escrow) Pay(deal types.Hash, provider types.Address, amount uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locked[deal] < amount {
		return fmt.Errorf("escrow: deal %s has %d locked, cannot pay %d",
			deal.Hex()[:10], e.locked[deal], amount)
	}
	e.locked[deal] -= amount
	e.paid[provider] += amount
	e.paidByDeal[deal] += amount
	return nil
}

// PaidForDeal returns the total paid out of a deal's escrow.
func (e *Escrow) PaidForDeal(deal types.Hash) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paidByDeal[deal]
}

// RefundRemainder returns the still-locked balance to the submitter and
// zeroes the lock. Idempotent.
func (e *Escrow) RefundRemainder(deal types.Hash) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rem := e.locked[deal]; rem > 0 {
		e.refunded[deal] += rem
		e.locked[deal] = 0
	}
}

// PaidTo returns cumulative payouts to a provider.
func (e *Escrow) PaidTo(provider types.Address) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paid[provider]
}

// Refunded returns the total refunded for a deal.
func (e *Escrow) Refunded(deal types.Hash) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refunded[deal]
}

// Locked returns the still-locked balance for a deal.
func (e *Escrow) Locked(deal types.Hash) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.locked[deal]
}

// TotalLocked returns the sum of all still-locked balances.
func (e *Escrow) TotalLocked() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var total uint64
	for _, v := range e.locked {
		total += v
	}
	return total
}
