// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package wallet implements the AI Agent Wallet Protocol, providing on-chain
// account management with session keys, spending policies, and gas sponsorship
// for autonomous AI agents.
package wallet

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// MaxSessionKeys is the maximum number of session keys an agent account can hold.
const MaxSessionKeys = 16

// Errors returned by account operations.
var (
	ErrMaxSessionKeys    = errors.New("wallet: maximum session keys reached")
	ErrSessionKeyExpired = errors.New("wallet: session key expired")
	ErrSessionKeyRevoked = errors.New("wallet: session key revoked")
	ErrSessionKeyUnknown = errors.New("wallet: session key not found")
	ErrSpendLimitExceed  = errors.New("wallet: spend limit exceeded")
	ErrContractDenied    = errors.New("wallet: target contract not allowed")
	ErrMethodDenied      = errors.New("wallet: method selector not allowed")
	ErrGasLimitExceed    = errors.New("wallet: gas exceeds session key limit")
	ErrNilOwnerKey       = errors.New("wallet: owner key must not be nil")
	ErrEmptyDID          = errors.New("wallet: agent DID must not be empty")
)

// Account manages an AI agent's on-chain wallet.
type Account struct {
	mu          sync.RWMutex
	Owner       types.Address
	AgentDID    string
	Address     types.Address
	SessionKeys map[types.Hash]*SessionKey
	Policies    []SpendingPolicy
	CreatedAt   time.Time
}

// SessionKey is a temporary key with constrained permissions.
type SessionKey struct {
	ID          types.Hash
	PublicKey   []byte
	Permissions SessionPermissions
	Expiry      time.Time
	SpendLimit  *uint256.Int
	SpentAmount *uint256.Int
	Revoked     bool
	CreatedAt   time.Time
}

// SessionPermissions defines the allowed operations for a session key.
type SessionPermissions struct {
	AllowedContracts []types.Address
	AllowedMethods   [][]byte // 4-byte method selectors
	MaxGasPerTx      uint64
}

// NewAccount creates a new agent account. The agent address is
// deterministically derived from the owner key and agent DID using Keccak256.
func NewAccount(ownerKey []byte, agentDID string) (*Account, error) {
	if len(ownerKey) == 0 {
		return nil, ErrNilOwnerKey
	}
	if agentDID == "" {
		return nil, ErrEmptyDID
	}

	// Derive the owner address from the owner key.
	ownerAddr := types.BytesToAddress(crypto.Keccak256(ownerKey)[12:])

	// Derive a deterministic agent address from ownerKey + DID.
	agentAddr := types.BytesToAddress(
		crypto.Keccak256(ownerKey, []byte(agentDID))[12:],
	)

	return &Account{
		Owner:       ownerAddr,
		AgentDID:    agentDID,
		Address:     agentAddr,
		SessionKeys: make(map[types.Hash]*SessionKey),
		CreatedAt:   time.Now(),
	}, nil
}

// CreateSessionKey creates a new session key with the given constraints.
// Returns an error if the maximum number of session keys has been reached.
func (a *Account) CreateSessionKey(
	permissions SessionPermissions,
	expiry time.Time,
	spendLimit *uint256.Int,
) (*SessionKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.SessionKeys) >= MaxSessionKeys {
		return nil, ErrMaxSessionKeys
	}

	// Generate random key material for the session key.
	pubKey := make([]byte, 33) // compressed public key length
	if _, err := rand.Read(pubKey); err != nil {
		return nil, fmt.Errorf("wallet: failed to generate session key: %w", err)
	}

	// Derive a unique ID from the key material and a timestamp nonce.
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], uint64(time.Now().UnixNano()))
	keyID := crypto.Keccak256Hash(pubKey, nonceBuf[:])

	limit := uint256.NewInt(0)
	if spendLimit != nil {
		limit.Set(spendLimit)
	}

	sk := &SessionKey{
		ID:          keyID,
		PublicKey:   pubKey,
		Permissions: permissions,
		Expiry:      expiry,
		SpendLimit:  limit,
		SpentAmount: uint256.NewInt(0),
		Revoked:     false,
		CreatedAt:   time.Now(),
	}

	a.SessionKeys[keyID] = sk
	return sk, nil
}

// ValidateSessionKey validates that the session key identified by keyID is
// authorized to perform the specified operation. It checks expiry, revocation
// status, spend limits, allowed contracts, allowed methods, and gas limits.
func (a *Account) ValidateSessionKey(
	keyID types.Hash,
	target types.Address,
	methodSig []byte,
	value *uint256.Int,
	gas uint64,
) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	sk, ok := a.SessionKeys[keyID]
	if !ok {
		return ErrSessionKeyUnknown
	}

	if sk.Revoked {
		return ErrSessionKeyRevoked
	}

	if time.Now().After(sk.Expiry) {
		return ErrSessionKeyExpired
	}

	// Check spend limit: spentAmount + value must not exceed limit.
	if err := checkSpendLimit(sk, value); err != nil {
		return err
	}

	// Check allowed contracts (empty list means all contracts are allowed).
	if len(sk.Permissions.AllowedContracts) > 0 {
		allowed := false
		for _, c := range sk.Permissions.AllowedContracts {
			if c == target {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrContractDenied
		}
	}

	// Check allowed methods (empty list means all methods are allowed).
	if len(sk.Permissions.AllowedMethods) > 0 {
		if len(methodSig) < 4 {
			return fmt.Errorf("wallet: method selector required when method restrictions are set")
		}
		allowed := false
		sig := methodSig[:4]
		for _, m := range sk.Permissions.AllowedMethods {
			if len(m) >= 4 && m[0] == sig[0] && m[1] == sig[1] && m[2] == sig[2] && m[3] == sig[3] {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrMethodDenied
		}
	}

	// Check gas limit (zero means no gas restriction).
	if sk.Permissions.MaxGasPerTx > 0 && gas > sk.Permissions.MaxGasPerTx {
		return ErrGasLimitExceed
	}

	return nil
}

// RevokeSessionKey marks the session key identified by keyID as revoked.
func (a *Account) RevokeSessionKey(keyID types.Hash) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	sk, ok := a.SessionKeys[keyID]
	if !ok {
		return ErrSessionKeyUnknown
	}

	sk.Revoked = true
	return nil
}

// GetSessionKey retrieves the session key identified by keyID.
func (a *Account) GetSessionKey(keyID types.Hash) (*SessionKey, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	sk, ok := a.SessionKeys[keyID]
	if !ok {
		return nil, ErrSessionKeyUnknown
	}

	return sk, nil
}

// ActiveSessionKeys returns all session keys that are neither revoked nor expired.
func (a *Account) ActiveSessionKeys() []*SessionKey {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := time.Now()
	active := make([]*SessionKey, 0, len(a.SessionKeys))
	for _, sk := range a.SessionKeys {
		if !sk.Revoked && now.Before(sk.Expiry) {
			active = append(active, sk)
		}
	}

	return active
}

// RecordSpend atomically adds the given value to the session key's spent amount.
// It must be called after a successful validation to track cumulative spending.
func (a *Account) RecordSpend(keyID types.Hash, value *uint256.Int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	sk, ok := a.SessionKeys[keyID]
	if !ok {
		return ErrSessionKeyUnknown
	}

	if err := checkSpendLimit(sk, value); err != nil {
		return err
	}
	if value != nil && !value.IsZero() {
		sk.SpentAmount.Add(sk.SpentAmount, value)
	}

	return nil
}

// checkSpendLimit returns ErrSpendLimitExceed if adding value to the session
// key's spent amount would exceed its limit. Must be called under the
// account lock.
func checkSpendLimit(sk *SessionKey, value *uint256.Int) error {
	if value != nil && !value.IsZero() {
		// uint256 Add is modular: a value near 2^256-1 would wrap projected
		// BELOW SpentAmount and slip under the limit. Detect the carry (overflow)
		// and reject — an overflowing spend can never be within any real limit.
		projected, overflow := new(uint256.Int).AddOverflow(sk.SpentAmount, value)
		if overflow || projected.Cmp(sk.SpendLimit) > 0 {
			return ErrSpendLimitExceed
		}
	}
	return nil
}
