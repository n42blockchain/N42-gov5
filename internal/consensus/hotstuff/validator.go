// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/common/crypto/bls/common"
	"github.com/n42blockchain/N42/common/types"
)

// ValidatorInfo holds the address and BLS public key of a validator.
type ValidatorInfo struct {
	Address   types.Address
	PublicKey common.PublicKey
}

// ValidatorSet manages the active validator set for consensus.
// Validators are indexed by position (0..n-1). The set is fixed within an epoch.
type ValidatorSet struct {
	validators     []ValidatorInfo
	faultTolerance uint32 // f = (n-1)/3
}

// NewValidatorSet creates a new validator set.
// faultTolerance should be (n-1)/3 for BFT safety.
func NewValidatorSet(validators []ValidatorInfo, faultTolerance uint32) *ValidatorSet {
	entries := make([]ValidatorInfo, len(validators))
	copy(entries, validators)
	return &ValidatorSet{
		validators:     entries,
		faultTolerance: faultTolerance,
	}
}

// Len returns the number of validators.
func (vs *ValidatorSet) Len() uint32 {
	return uint32(len(vs.validators))
}

// IsEmpty returns true if the set is empty.
func (vs *ValidatorSet) IsEmpty() bool {
	return len(vs.validators) == 0
}

// QuorumSize returns 2f+1, the minimum number of votes for quorum.
func (vs *ValidatorSet) QuorumSize() int {
	return int(2*uint64(vs.faultTolerance) + 1)
}

// FaultTolerance returns f.
func (vs *ValidatorSet) FaultTolerance() uint32 {
	return vs.faultTolerance
}

// GetPublicKey returns the BLS public key for a validator by index.
func (vs *ValidatorSet) GetPublicKey(index ValidatorIndex) (common.PublicKey, error) {
	if int(index) >= len(vs.validators) {
		return nil, &UnknownValidatorError{Index: index, SetSize: vs.Len()}
	}
	return vs.validators[index].PublicKey, nil
}

// GetAddress returns the address for a validator by index.
func (vs *ValidatorSet) GetAddress(index ValidatorIndex) (types.Address, error) {
	if int(index) >= len(vs.validators) {
		return types.Address{}, &UnknownValidatorError{Index: index, SetSize: vs.Len()}
	}
	return vs.validators[index].Address, nil
}

// Contains checks if a validator index is valid.
func (vs *ValidatorSet) Contains(index ValidatorIndex) bool {
	return int(index) < len(vs.validators)
}

// FindByAddress returns the validator index for the given address, or -1 if not found.
func (vs *ValidatorSet) FindByAddress(addr types.Address) int {
	for i, v := range vs.validators {
		if v.Address == addr {
			return i
		}
	}
	return -1
}

// Addresses returns a sorted list of all validator addresses.
func (vs *ValidatorSet) Addresses() []types.Address {
	addrs := make([]types.Address, len(vs.validators))
	for i, v := range vs.validators {
		addrs[i] = v.Address
	}
	sort.Slice(addrs, func(i, j int) bool {
		return addrs[i].Hex() < addrs[j].Hex()
	})
	return addrs
}

// Clone returns a deep copy of the validator set.
func (vs *ValidatorSet) Clone() *ValidatorSet {
	entries := make([]ValidatorInfo, len(vs.validators))
	copy(entries, vs.validators)
	return &ValidatorSet{
		validators:     entries,
		faultTolerance: vs.faultTolerance,
	}
}

// LeaderForView returns the leader's validator index for the given view.
// Uses round-robin: leader = view % n.
func LeaderForView(view ViewNumber, vs *ValidatorSet) ValidatorIndex {
	if vs.IsEmpty() {
		return 0
	}
	return ValidatorIndex(view % uint64(vs.Len()))
}

// IsLeader checks if the given validator is the leader for the given view.
func IsLeader(validatorIndex ValidatorIndex, view ViewNumber, vs *ValidatorSet) bool {
	return LeaderForView(view, vs) == validatorIndex
}

// EpochManager manages validator set transitions across epochs.
type EpochManager struct {
	epochLength    uint64
	currentEpoch   uint64
	currentSet     *ValidatorSet
	nextSet        *ValidatorSet
	historicalSets map[uint64]*ValidatorSet
}

const maxHistoricalEpochs = 3

// NewEpochManager creates an EpochManager with epochs disabled (single static validator set).
func NewEpochManager(validatorSet *ValidatorSet) *EpochManager {
	return &EpochManager{
		epochLength:    0,
		currentEpoch:   0,
		currentSet:     validatorSet,
		historicalSets: make(map[uint64]*ValidatorSet),
	}
}

// NewEpochManagerWithLength creates an EpochManager with epoch transitions enabled.
func NewEpochManagerWithLength(validatorSet *ValidatorSet, epochLength uint64) *EpochManager {
	return &EpochManager{
		epochLength:    epochLength,
		currentEpoch:   0,
		currentSet:     validatorSet,
		historicalSets: make(map[uint64]*ValidatorSet),
	}
}

// EpochLength returns the epoch length (0 = disabled).
func (em *EpochManager) EpochLength() uint64 {
	return em.epochLength
}

// CurrentEpoch returns the current epoch number.
func (em *EpochManager) CurrentEpoch() uint64 {
	return em.currentEpoch
}

// CurrentValidatorSet returns the active validator set.
func (em *EpochManager) CurrentValidatorSet() *ValidatorSet {
	return em.currentSet
}

// EpochsEnabled returns true if epoch transitions are active.
func (em *EpochManager) EpochsEnabled() bool {
	return em.epochLength > 0
}

// EpochForView computes which epoch a given view belongs to.
func (em *EpochManager) EpochForView(view uint64) uint64 {
	if em.epochLength == 0 {
		return 0
	}
	if view == 0 {
		return 0
	}
	return (view - 1) / em.epochLength
}

// IsEpochBoundary checks if a given view is the first view of a new epoch.
func (em *EpochManager) IsEpochBoundary(view uint64) bool {
	if em.epochLength == 0 || view <= 1 {
		return false
	}
	return (view-1)%em.epochLength == 0
}

// ValidatorSetForView returns the validator set for verifying QCs at the given view.
func (em *EpochManager) ValidatorSetForView(view uint64) *ValidatorSet {
	if em.epochLength == 0 {
		return em.currentSet
	}
	epoch := em.EpochForView(view)
	if epoch == em.currentEpoch {
		return em.currentSet
	}
	if set, ok := em.historicalSets[epoch]; ok {
		return set
	}
	return em.currentSet
}

// StageNextEpoch stages a new validator set for the next epoch.
func (em *EpochManager) StageNextEpoch(validators []ValidatorInfo, faultTolerance uint32) {
	em.nextSet = NewValidatorSet(validators, faultTolerance)
}

// HasStagedNext returns whether a next epoch validator set has been staged.
func (em *EpochManager) HasStagedNext() bool {
	return em.nextSet != nil
}

// AdvanceEpoch activates the staged validator set.
// Returns true if a transition occurred.
func (em *EpochManager) AdvanceEpoch() bool {
	if em.nextSet == nil {
		return false
	}
	em.historicalSets[em.currentEpoch] = em.currentSet.Clone()
	em.trimHistorical()
	em.currentEpoch++
	em.currentSet = em.nextSet
	em.nextSet = nil
	return true
}

func (em *EpochManager) trimHistorical() {
	for len(em.historicalSets) > maxHistoricalEpochs {
		var oldest uint64
		first := true
		for epoch := range em.historicalSets {
			if first || epoch < oldest {
				oldest = epoch
				first = false
			}
		}
		if !first {
			delete(em.historicalSets, oldest)
		}
	}
}

// String returns a human-readable representation.
func (em *EpochManager) String() string {
	return fmt.Sprintf("EpochManager{epoch=%d, validators=%d, epochLength=%d}",
		em.currentEpoch, em.currentSet.Len(), em.epochLength)
}
