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

// Package consensus provides the consensus engine interfaces and common utilities.
package consensus

import (
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
)

// BasePoA contains common fields and logic for PoA consensus engines.
// This struct should be embedded in concrete consensus engine implementations.
//
// Note: SignerFn is defined in each engine package (apoa/apos) to avoid import cycles,
// as it references accounts.Account which creates a dependency cycle.
type BasePoA struct {
	db         kv.RwDB       // Database to store and retrieve snapshot checkpoints
	recents    *lru.ARCCache // Snapshots for recent block to speed up reorgs
	signatures *lru.ARCCache // Signatures of recent blocks to speed up mining

	proposals map[types.Address]bool // Current list of proposals we are pushing

	signer types.Address // Ethereum address of the signing key
	lock   sync.RWMutex  // Protects the signer and proposals fields

	// The fields below are for testing only
	FakeDiff bool // Skip difficulty verifications

	// Header validator for common validation logic
	validator *misc.HeaderValidator
}

// NewBasePoA creates a new BasePoA with the given parameters.
func NewBasePoA(db kv.RwDB, epoch uint64) *BasePoA {
	recents, _ := lru.NewARC(misc.InmemorySnapshots)
	signatures, _ := lru.NewARC(misc.InmemorySignatures)

	return &BasePoA{
		db:         db,
		recents:    recents,
		signatures: signatures,
		proposals:  make(map[types.Address]bool),
		validator:  misc.NewHeaderValidator(epoch),
	}
}

// Database returns the underlying database.
func (b *BasePoA) Database() kv.RwDB {
	return b.db
}

// Recents returns the snapshot cache.
func (b *BasePoA) Recents() *lru.ARCCache {
	return b.recents
}

// Signatures returns the signature cache.
func (b *BasePoA) Signatures() *lru.ARCCache {
	return b.signatures
}

// Proposals returns the current proposals map.
func (b *BasePoA) Proposals() map[types.Address]bool {
	b.lock.RLock()
	defer b.lock.RUnlock()
	// Return a copy to avoid race conditions
	copy := make(map[types.Address]bool, len(b.proposals))
	for k, v := range b.proposals {
		copy[k] = v
	}
	return copy
}

// SetProposal sets a proposal for the given address.
func (b *BasePoA) SetProposal(address types.Address, authorize bool) {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.proposals[address] = authorize
}

// DeleteProposal removes a proposal for the given address.
func (b *BasePoA) DeleteProposal(address types.Address) {
	b.lock.Lock()
	defer b.lock.Unlock()
	delete(b.proposals, address)
}

// Signer returns the current signer address.
func (b *BasePoA) Signer() types.Address {
	b.lock.RLock()
	defer b.lock.RUnlock()
	return b.signer
}

// SetSigner sets the signer address.
// Note: Full Authorize() with SignerFn is in each engine package due to import cycles.
func (b *BasePoA) SetSigner(signer types.Address) {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.signer = signer
}

// Validator returns the header validator.
func (b *BasePoA) Validator() *misc.HeaderValidator {
	return b.validator
}

// WithLock executes a function while holding the read lock.
func (b *BasePoA) WithLock(fn func()) {
	b.lock.Lock()
	defer b.lock.Unlock()
	fn()
}

// WithRLock executes a function while holding the read lock.
func (b *BasePoA) WithRLock(fn func()) {
	b.lock.RLock()
	defer b.lock.RUnlock()
	fn()
}

// VerifyHeadersAsync is a common implementation for batch header verification.
// It returns a quit channel to abort the operations and a results channel to
// retrieve the async verifications (the order is that of the input slice).
func VerifyHeadersAsync(
	headers []block.IHeader,
	verifyFn func(header block.IHeader, parents []block.IHeader) error,
) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))

	go func() {
		for i, header := range headers {
			err := verifyFn(header, headers[:i])

			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()
	return abort, results
}

// CalcDifficultyWithSnapshot calculates difficulty using a snapshot that implements Inturn.
func CalcDifficultyWithSnapshot(snap misc.Inturn, blockNumber uint64, signer types.Address) *uint256.Int {
	return misc.CalcDifficulty(snap, blockNumber, signer)
}

// Author extracts the Ethereum account address from a signed header using the signature cache.
func (b *BasePoA) Author(header block.IHeader) (types.Address, error) {
	return misc.Ecrecover(header, b.signatures)
}

// SealHash returns the hash of a block prior to it being sealed.
func (b *BasePoA) SealHash(header block.IHeader) types.Hash {
	return misc.SealHash(header)
}

// Close is a noop for PoA engines as there are no background threads.
func (b *BasePoA) Close() error {
	return nil
}

// =============================================================================
// Compile-time Interface Compliance
// =============================================================================

// BasePoAInterface defines the interface that BasePoA provides.
// This is for documentation and type checking purposes.
// Concrete engines (apoa, apos) should embed BasePoA and implement
// the remaining Engine methods.
type BasePoAInterface interface {
	// Database access
	Database() kv.RwDB
	Recents() *lru.ARCCache
	Signatures() *lru.ARCCache

	// Proposal management
	Proposals() map[types.Address]bool
	SetProposal(address types.Address, authorize bool)
	DeleteProposal(address types.Address)

	// Signer management
	Signer() types.Address
	SetSigner(signer types.Address)

	// Header validator
	Validator() *misc.HeaderValidator

	// Common Engine methods
	Author(header block.IHeader) (types.Address, error)
	SealHash(header block.IHeader) types.Hash
	Close() error
}

// Compile-time check: BasePoA must implement BasePoAInterface
var _ BasePoAInterface = (*BasePoA)(nil)

