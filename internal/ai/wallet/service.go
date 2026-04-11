// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service: top-level coordinator for the AI Agent Wallet protocol.
// Owns Account records, session keys, attached SpendingPolicy
// instances and the PaymasterService. Exposes CreateAccount,
// lookup and per-account mutation helpers while guarding access
// with ErrAccountNotFound / ErrAccountExists.

package wallet

import (
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Errors returned by the agent service.
var (
	ErrAccountNotFound = errors.New("wallet: account not found")
	ErrAccountExists   = errors.New("wallet: account already exists")
)

// Service is the top-level coordinator for the AI Agent Wallet Protocol.
// It manages agent accounts, session keys, spending policies, and gas
// sponsorship through the integrated paymaster.
type Service struct {
	mu        sync.RWMutex
	accounts  map[types.Address]*Account
	paymaster *PaymasterService
}

// NewService creates a new agent service. If paymasterEnabled is true, a
// PaymasterService is initialized for gas sponsorship. maxSessionKeys
// controls the upper bound on session keys per account (capped to
// MaxSessionKeys if it exceeds the constant).
func NewService(maxSessionKeys int, paymasterEnabled bool) *Service {
	s := &Service{
		accounts: make(map[types.Address]*Account),
	}

	if paymasterEnabled {
		s.paymaster = NewPaymasterService()
	}

	return s
}

// CreateAccount creates a new agent account derived from the given owner key
// and agent DID, and registers it in the service. Returns an error if an
// account with the same derived address already exists.
func (s *Service) CreateAccount(ownerKey []byte, agentDID string) (*Account, error) {
	acct, err := NewAccount(ownerKey, agentDID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.accounts[acct.Address]; exists {
		return nil, ErrAccountExists
	}

	s.accounts[acct.Address] = acct
	return acct, nil
}

// GetAccount retrieves the agent account for the given address.
func (s *Service) GetAccount(addr types.Address) (*Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	acct, ok := s.accounts[addr]
	if !ok {
		return nil, ErrAccountNotFound
	}

	return acct, nil
}

// RemoveAccount removes the agent account at the given address.
func (s *Service) RemoveAccount(addr types.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accounts[addr]; !ok {
		return ErrAccountNotFound
	}

	delete(s.accounts, addr)
	return nil
}

// Paymaster returns the paymaster service, or nil if gas sponsorship is disabled.
func (s *Service) Paymaster() *PaymasterService {
	return s.paymaster
}

// AccountCount returns the number of registered agent accounts.
func (s *Service) AccountCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}

// Accounts returns a snapshot of all registered agent addresses.
func (s *Service) Accounts() []types.Address {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addrs := make([]types.Address, 0, len(s.accounts))
	for addr := range s.accounts {
		addrs = append(addrs, addr)
	}
	return addrs
}
