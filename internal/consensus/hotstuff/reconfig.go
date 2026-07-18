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
// ReconfigurationManager for safe validator set changes.
// Implements the commit-then-activate protocol from HotStuff-2 and
// Jolteon: PROPOSE a change, COMMIT it with the current set under a
// CommitQC, STAGE the new set at the epoch boundary via
// EpochManager.StageNextEpoch, and ACTIVATE on the first view of the
// next epoch. Ensures deterministic, fault-tolerant committee rotation.

package hotstuff

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// ReconfigurationManager handles safe validator set changes following the
// commit-then-activate protocol from the HotStuff/Jolteon literature.
//
// Protocol overview (based on HotStuff-2 § 5 and Jolteon § 4.3):
//
//  1. PROPOSE: A reconfiguration proposal (add/remove validator) is submitted
//     via on-chain transaction or governance call.
//
//  2. COMMIT: The block containing the reconfiguration is committed by the
//     CURRENT validator set with a CommitQC. This ensures the change is
//     finalized before taking effect.
//
//  3. STAGE: At the epoch boundary, all nodes deterministically compute the
//     new validator set from the committed state. The new set is staged via
//     EpochManager.StageNextEpoch().
//
//  4. ACTIVATE: On the first view of the new epoch, the staged set becomes
//     active via EpochManager.AdvanceEpoch(). All subsequent messages use the
//     new quorum parameters.
//
// Safety invariants:
//   - The old validator set must finalize the reconfiguration block
//   - All honest nodes see the same new set (deterministic from committed state)
//   - QCs from the transition boundary are verified against the epoch-appropriate set
//   - Historical sets are retained for QC verification across epoch boundaries
type ReconfigurationManager struct {
	mu           sync.Mutex
	epochManager *EpochManager

	// pendingAdds holds validators to add at the next epoch boundary.
	pendingAdds []ValidatorInfo
	// pendingRemoves holds addresses to remove at the next epoch boundary.
	pendingRemoves []types.Address
	// committed tracks whether pending changes have been committed (CommitQC).
	committed bool
}

// NewReconfigurationManager creates a ReconfigurationManager for the given EpochManager.
func NewReconfigurationManager(em *EpochManager) *ReconfigurationManager {
	return &ReconfigurationManager{
		epochManager: em,
	}
}

// ProposeAddValidator queues a validator for addition at the next epoch boundary.
// The change does NOT take effect until it is committed and the epoch advances.
//
// Per HotStuff-2 reconfiguration safety:
//   - The proposal must be included in a block
//   - The block must be committed by the current validator set (CommitQC)
//   - The new validator set is computed deterministically from committed state
func (rm *ReconfigurationManager) ProposeAddValidator(addr types.Address, pubKey common.PublicKey) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if pubKey == nil {
		return fmt.Errorf("reconfig: BLS public key required for validator %s", addr.Hex())
	}

	// Guard: no proposals when a transition is already staged (Rust: EpochTransitionAlreadyStaged)
	if rm.epochManager.HasStagedNext() {
		return fmt.Errorf("reconfig: epoch transition already staged, wait for epoch boundary before proposing")
	}

	// Check not already in current set
	if rm.epochManager.CurrentValidatorSet().FindByAddress(addr) >= 0 {
		return fmt.Errorf("reconfig: validator %s already in active set", addr.Hex())
	}

	// Check not already pending
	for _, v := range rm.pendingAdds {
		if v.Address == addr {
			return fmt.Errorf("reconfig: validator %s already pending addition", addr.Hex())
		}
	}

	rm.pendingAdds = append(rm.pendingAdds, ValidatorInfo{
		Address:   addr,
		PublicKey: pubKey,
	})
	rm.committed = false

	log.Info("Validator addition proposed",
		"address", addr.Hex(),
		"pendingAdds", len(rm.pendingAdds),
		"epoch", rm.epochManager.CurrentEpoch(),
	)
	return nil
}

// ProposeRemoveValidator queues a validator for removal at the next epoch boundary.
//
// Safety constraints:
//   - Cannot remove below minimum validator count (4 for f=1)
//   - Cannot remove if it would break the 3f+1 requirement
//   - The removal must be committed before taking effect
func (rm *ReconfigurationManager) ProposeRemoveValidator(addr types.Address) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Guard: no proposals when a transition is already staged
	if rm.epochManager.HasStagedNext() {
		return fmt.Errorf("reconfig: epoch transition already staged, wait for epoch boundary before proposing")
	}

	// Check exists in current set
	if rm.epochManager.CurrentValidatorSet().FindByAddress(addr) < 0 {
		return fmt.Errorf("reconfig: validator %s not in active set", addr.Hex())
	}

	// Check not already pending removal
	for _, a := range rm.pendingRemoves {
		if a == addr {
			return fmt.Errorf("reconfig: validator %s already pending removal", addr.Hex())
		}
	}

	// Compute resulting set size conservatively: only count current set minus
	// pending removes. Do NOT count pending adds since they may not materialize.
	currentN := int(rm.epochManager.CurrentValidatorSet().Len())
	resultN := currentN - len(rm.pendingRemoves) - 1
	if resultN < 4 {
		return fmt.Errorf("reconfig: cannot remove validator %s, would leave %d validators (minimum 4 for BFT safety)", addr.Hex(), resultN)
	}

	rm.pendingRemoves = append(rm.pendingRemoves, addr)
	rm.committed = false

	log.Info("Validator removal proposed",
		"address", addr.Hex(),
		"pendingRemoves", len(rm.pendingRemoves),
		"epoch", rm.epochManager.CurrentEpoch(),
	)
	return nil
}

// MarkCommitted records that the pending changes have been included in a
// committed block (received CommitQC). This is the safety gate: changes
// only take effect after commitment by the current validator set.
func (rm *ReconfigurationManager) MarkCommitted() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.hasPendingChanges() {
		rm.committed = true
		log.Info("Reconfiguration changes committed",
			"adds", len(rm.pendingAdds),
			"removes", len(rm.pendingRemoves),
		)
	}
}

// HasPendingChanges returns true if there are uncommitted or committed
// but not yet activated changes.
func (rm *ReconfigurationManager) HasPendingChanges() bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.hasPendingChanges()
}

func (rm *ReconfigurationManager) hasPendingChanges() bool {
	return len(rm.pendingAdds) > 0 || len(rm.pendingRemoves) > 0
}

// IsCommitted returns true if pending changes have been committed.
func (rm *ReconfigurationManager) IsCommitted() bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.isCommitted()
}

func (rm *ReconfigurationManager) isCommitted() bool {
	return rm.committed && rm.hasPendingChanges()
}

// ApplyAtEpochBoundary computes and stages the new validator set for the
// next epoch. This should be called at epoch boundaries ONLY after the
// changes have been committed.
//
// Returns the new validator set if changes were applied, nil otherwise.
//
// Per Jolteon § 4.3: "The reconfiguration is committed as part of the
// regular consensus protocol. Once committed, all honest replicas compute
// the new configuration deterministically."
func (rm *ReconfigurationManager) ApplyAtEpochBoundary() *ValidatorSet {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if !rm.isCommitted() {
		return nil
	}
	if !rm.epochManager.EpochsEnabled() {
		log.Warn("Reconfiguration ignored: epochs not enabled")
		return nil
	}

	currentSet := rm.epochManager.CurrentValidatorSet()

	// Build new validator list: start with current, apply removes, then adds
	newValidators := make([]ValidatorInfo, 0, int(currentSet.Len())+len(rm.pendingAdds))

	// Copy existing validators, excluding removed ones
	removeSet := make(map[types.Address]bool, len(rm.pendingRemoves))
	for _, addr := range rm.pendingRemoves {
		removeSet[addr] = true
	}

	for i := 0; i < int(currentSet.Len()); i++ {
		addr, _ := currentSet.GetAddress(ValidatorIndex(i))
		if !removeSet[addr] {
			pk, _ := currentSet.GetPublicKey(ValidatorIndex(i))
			newValidators = append(newValidators, ValidatorInfo{
				Address:   addr,
				PublicKey: pk,
			})
		}
	}

	// Add new validators
	newValidators = append(newValidators, rm.pendingAdds...)

	// Sort by raw address bytes for deterministic ordering across all nodes.
	// Do NOT use Address.Hex() which applies EIP-55 checksumming.
	sort.Slice(newValidators, func(i, j int) bool {
		return bytes.Compare(newValidators[i].Address[:], newValidators[j].Address[:]) < 0
	})

	// Compute fault tolerance: f = (n-1)/3
	n := uint32(len(newValidators))
	f := (n - 1) / 3

	if n < 4 {
		log.Error("Reconfiguration would produce unsafe validator count",
			"n", n, "minRequired", 4)
		return nil
	}

	newSet := NewValidatorSet(newValidators, f)

	// Validate transition BEFORE staging (HotStuff-2 safety).
	// If validation fails, do NOT stage the unsafe set.
	if err := ValidateTransition(currentSet, newSet); err != nil {
		log.Error("Unsafe validator set transition blocked", "err", err)
		return nil
	}

	// Stage the validated set
	rm.epochManager.StageNextEpoch(newValidators, f)

	addCount := len(rm.pendingAdds)
	removeCount := len(rm.pendingRemoves)

	// Clear pending changes
	rm.pendingAdds = nil
	rm.pendingRemoves = nil
	rm.committed = false

	log.Info("New validator set staged for next epoch",
		"epoch", rm.epochManager.CurrentEpoch()+1,
		"validators", n,
		"faultTolerance", f,
		"quorum", n-f,
		"added", addCount,
		"removed", removeCount,
	)

	return newSet
}

// PendingAddCount returns the number of validators pending addition.
func (rm *ReconfigurationManager) PendingAddCount() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return len(rm.pendingAdds)
}

// PendingRemoveCount returns the number of validators pending removal.
func (rm *ReconfigurationManager) PendingRemoveCount() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return len(rm.pendingRemoves)
}

// ValidateTransition checks that a proposed validator set transition is safe.
// This implements the safety checks from HotStuff-2 § 5:
//   - New set has at least 4 validators (for f >= 1)
//   - Quorum size is correctly computed as 2f+1
//   - There is sufficient overlap between old and new sets for liveness
func ValidateTransition(oldSet, newSet *ValidatorSet) error {
	if newSet.Len() < 4 {
		return fmt.Errorf("new validator set too small: %d < 4", newSet.Len())
	}

	expectedF := (newSet.Len() - 1) / 3
	if newSet.FaultTolerance() != expectedF {
		return fmt.Errorf("fault tolerance mismatch: have %d, want %d",
			newSet.FaultTolerance(), expectedF)
	}

	// Check overlap: at least 2f+1 validators from old set should be in new set
	// for liveness during the transition (Jolteon § 4.3 overlap requirement)
	if oldSet != nil && !oldSet.IsEmpty() {
		overlapCount := 0
		for i := 0; i < int(oldSet.Len()); i++ {
			addr, _ := oldSet.GetAddress(ValidatorIndex(i))
			if newSet.FindByAddress(addr) >= 0 {
				overlapCount++
			}
		}
		minOverlap := oldSet.QuorumSize()
		if overlapCount < minOverlap {
			return fmt.Errorf("insufficient validator overlap for safe transition: %d < %d (quorum)",
				overlapCount, minOverlap)
		}
	}

	return nil
}
