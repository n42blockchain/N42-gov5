// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package wallet

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// Errors returned by paymaster operations.
var (
	ErrInsufficientDeposit = errors.New("paymaster: insufficient deposit balance")
	ErrZeroAmount          = errors.New("paymaster: amount must be greater than zero")
	ErrNoDeposit           = errors.New("paymaster: no deposit found for address")
)

// PaymasterService manages gas sponsorship for AI agent operations.
// Owners deposit funds into the paymaster, which then sponsors gas costs
// on behalf of agent wallets.
type PaymasterService struct {
	mu        sync.RWMutex
	deposits  map[types.Address]*uint256.Int // deposit pool per owner
	sponsored uint64                         // total operations sponsored (atomic)
	nonce     uint64                         // monotonic nonce for paymaster data
}

// NewPaymasterService creates a new paymaster service with an empty deposit pool.
func NewPaymasterService() *PaymasterService {
	return &PaymasterService{
		deposits: make(map[types.Address]*uint256.Int),
	}
}

// Deposit adds funds to the deposit pool for the given owner address.
func (pm *PaymasterService) Deposit(owner types.Address, amount *uint256.Int) error {
	if amount == nil || amount.IsZero() {
		return ErrZeroAmount
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	bal, ok := pm.deposits[owner]
	if !ok {
		bal = uint256.NewInt(0)
		pm.deposits[owner] = bal
	}

	bal.Add(bal, amount)
	return nil
}

// WithdrawDeposit withdraws funds from the deposit pool for the given owner.
// Returns an error if the balance is insufficient.
func (pm *PaymasterService) WithdrawDeposit(owner types.Address, amount *uint256.Int) error {
	if amount == nil || amount.IsZero() {
		return ErrZeroAmount
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	bal, ok := pm.deposits[owner]
	if !ok {
		return ErrNoDeposit
	}

	if bal.Cmp(amount) < 0 {
		return ErrInsufficientDeposit
	}

	bal.Sub(bal, amount)

	// Remove entry if balance reaches zero to avoid map bloat.
	if bal.IsZero() {
		delete(pm.deposits, owner)
	}

	return nil
}

// SponsorOperation sponsors a gas operation for the given agent address by
// deducting gasCost from the agent's deposit. It returns opaque paymaster data
// containing the sponsor address, gas cost, and a monotonic nonce. The caller
// must have previously deposited sufficient funds under agentAddr.
func (pm *PaymasterService) SponsorOperation(agentAddr types.Address, gasCost *uint256.Int) ([]byte, error) {
	if gasCost == nil || gasCost.IsZero() {
		return nil, ErrZeroAmount
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	bal, ok := pm.deposits[agentAddr]
	if !ok {
		return nil, ErrNoDeposit
	}

	if bal.Cmp(gasCost) < 0 {
		return nil, ErrInsufficientDeposit
	}

	bal.Sub(bal, gasCost)

	// Remove entry if balance reaches zero.
	if bal.IsZero() {
		delete(pm.deposits, agentAddr)
	}

	// Increment the nonce for this operation.
	pm.nonce++
	nonce := pm.nonce

	// Increment the sponsored counter atomically.
	atomic.AddUint64(&pm.sponsored, 1)

	// Build paymaster data: sponsor address (20) + gas cost (32) + nonce (8).
	data := make([]byte, 0, types.AddressLength+32+8)
	data = append(data, agentAddr.Bytes()...)

	var gasBuf [32]byte
	gasCost.WriteToSlice(gasBuf[:])
	data = append(data, gasBuf[:]...)

	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)
	data = append(data, nonceBuf[:]...)

	// Return Keccak256-tagged paymaster data for integrity verification.
	tag := crypto.Keccak256(data)
	result := make([]byte, 0, len(data)+len(tag))
	result = append(result, data...)
	result = append(result, tag...)

	return result, nil
}

// Balance returns the current deposit balance for the given owner address.
// Returns zero if no deposit exists.
func (pm *PaymasterService) Balance(owner types.Address) *uint256.Int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	bal, ok := pm.deposits[owner]
	if !ok {
		return uint256.NewInt(0)
	}

	// Return a copy to prevent external mutation.
	return new(uint256.Int).Set(bal)
}

// TotalSponsored returns the total number of operations sponsored.
func (pm *PaymasterService) TotalSponsored() uint64 {
	return atomic.LoadUint64(&pm.sponsored)
}

// DepositCount returns the number of addresses with non-zero deposits.
func (pm *PaymasterService) DepositCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.deposits)
}
