package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/crypto/bls/blst"
	"github.com/n42blockchain/N42/common/types"
)

func makeTestValidators(n int) []ValidatorInfo {
	validators := make([]ValidatorInfo, n)
	for i := 0; i < n; i++ {
		sk, _ := blst.RandKey()
		validators[i] = ValidatorInfo{
			Address:   types.BytesToAddress([]byte{byte(i + 1)}),
			PublicKey: sk.PublicKey(),
		}
	}
	return validators
}

func TestReconfigAddValidator(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1) // f=1 for n=4
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Add a new validator
	newSK, _ := blst.RandKey()
	newAddr := types.BytesToAddress([]byte{0x10})
	err := rm.ProposeAddValidator(newAddr, newSK.PublicKey())
	if err != nil {
		t.Fatalf("ProposeAddValidator error: %v", err)
	}

	if !rm.HasPendingChanges() {
		t.Fatal("expected pending changes")
	}
	if rm.IsCommitted() {
		t.Fatal("should not be committed yet")
	}

	// Cannot apply before commit
	result := rm.ApplyAtEpochBoundary()
	if result != nil {
		t.Fatal("should not apply uncommitted changes")
	}

	// Mark committed (simulating CommitQC received)
	rm.MarkCommitted()
	if !rm.IsCommitted() {
		t.Fatal("should be committed")
	}

	// Apply at epoch boundary
	result = rm.ApplyAtEpochBoundary()
	if result == nil {
		t.Fatal("expected new validator set")
	}
	if result.Len() != 5 {
		t.Fatalf("expected 5 validators, got %d", result.Len())
	}
	if result.FaultTolerance() != 1 {
		t.Fatalf("expected f=1 for n=5, got f=%d", result.FaultTolerance())
	}
	if result.QuorumSize() != 3 {
		t.Fatalf("expected quorum=3 for n=5, got %d", result.QuorumSize())
	}

	// Verify new validator is in the set
	if result.FindByAddress(newAddr) < 0 {
		t.Fatal("new validator not found in set")
	}

	// Pending changes should be cleared
	if rm.HasPendingChanges() {
		t.Fatal("pending changes should be cleared after apply")
	}
}

func TestReconfigRemoveValidator(t *testing.T) {
	validators := makeTestValidators(7) // f=2, quorum=5
	vs := NewValidatorSet(validators, 2)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Remove one validator
	removeAddr := validators[3].Address
	err := rm.ProposeRemoveValidator(removeAddr)
	if err != nil {
		t.Fatalf("ProposeRemoveValidator error: %v", err)
	}

	rm.MarkCommitted()
	result := rm.ApplyAtEpochBoundary()
	if result == nil {
		t.Fatal("expected new validator set")
	}
	if result.Len() != 6 {
		t.Fatalf("expected 6 validators after removal, got %d", result.Len())
	}

	// Removed validator should not be in new set
	if result.FindByAddress(removeAddr) >= 0 {
		t.Fatal("removed validator still in set")
	}
}

func TestReconfigRejectRemoveBelowMinimum(t *testing.T) {
	validators := makeTestValidators(4) // minimum BFT set
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Try to remove - should fail (would leave 3 validators)
	err := rm.ProposeRemoveValidator(validators[0].Address)
	if err == nil {
		t.Fatal("expected error when removing below minimum")
	}
}

func TestReconfigRejectDuplicateAdd(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Try to add existing validator
	err := rm.ProposeAddValidator(validators[0].Address, validators[0].PublicKey)
	if err == nil {
		t.Fatal("expected error when adding existing validator")
	}
}

func TestReconfigAddAndRemoveInSameEpoch(t *testing.T) {
	validators := makeTestValidators(7)
	vs := NewValidatorSet(validators, 2)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Add one, remove one
	newSK, _ := blst.RandKey()
	newAddr := types.BytesToAddress([]byte{0x20})
	rm.ProposeAddValidator(newAddr, newSK.PublicKey())
	rm.ProposeRemoveValidator(validators[0].Address)

	rm.MarkCommitted()
	result := rm.ApplyAtEpochBoundary()
	if result == nil {
		t.Fatal("expected new validator set")
	}
	// 7 - 1 + 1 = 7
	if result.Len() != 7 {
		t.Fatalf("expected 7 validators, got %d", result.Len())
	}

	// Old validator removed
	if result.FindByAddress(validators[0].Address) >= 0 {
		t.Fatal("removed validator still present")
	}
	// New validator added
	if result.FindByAddress(newAddr) < 0 {
		t.Fatal("new validator not found")
	}
}

func TestValidateTransition(t *testing.T) {
	oldValidators := makeTestValidators(7) // f=2, quorum=5
	oldSet := NewValidatorSet(oldValidators, 2)

	// Valid transition: keep all, add one
	newValidators := append(makeTestValidators(0), oldValidators...)
	newSK, _ := blst.RandKey()
	newValidators = append(newValidators, ValidatorInfo{
		Address:   types.BytesToAddress([]byte{0x30}),
		PublicKey: newSK.PublicKey(),
	})
	newSet := NewValidatorSet(newValidators, 2)

	err := ValidateTransition(oldSet, newSet)
	if err != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}

	// Invalid transition: too few validators
	tinySet := NewValidatorSet(makeTestValidators(3), 0)
	err = ValidateTransition(oldSet, tinySet)
	if err == nil {
		t.Fatal("expected error for tiny validator set")
	}

	// Invalid transition: insufficient overlap (completely different addresses)
	disjointValidators := make([]ValidatorInfo, 7)
	for i := 0; i < 7; i++ {
		sk, _ := blst.RandKey()
		disjointValidators[i] = ValidatorInfo{
			Address:   types.BytesToAddress([]byte{byte(0xA0 + i)}), // different from oldSet
			PublicKey: sk.PublicKey(),
		}
	}
	completelyNewSet := NewValidatorSet(disjointValidators, 2)
	err = ValidateTransition(oldSet, completelyNewSet)
	if err == nil {
		t.Fatal("expected error for insufficient overlap")
	}
}

func TestReconfigDeterministicOrdering(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	// Add validators in reverse order - result should be sorted
	for i := 3; i >= 0; i-- {
		sk, _ := blst.RandKey()
		addr := types.BytesToAddress([]byte{byte(0x40 + i)})
		rm.ProposeAddValidator(addr, sk.PublicKey())
	}

	rm.MarkCommitted()
	result := rm.ApplyAtEpochBoundary()
	if result == nil {
		t.Fatal("expected new validator set")
	}

	// Verify addresses are sorted
	addrs := result.Addresses()
	for i := 1; i < len(addrs); i++ {
		if addrs[i].Hex() < addrs[i-1].Hex() {
			t.Fatalf("validators not sorted: %s >= %s at index %d",
				addrs[i-1].Hex(), addrs[i].Hex(), i)
		}
	}
}

func TestReconfigEpochIntegration(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 5) // 5 views per epoch
	rm := NewReconfigurationManager(em)

	// Add a validator and commit
	newSK, _ := blst.RandKey()
	newAddr := types.BytesToAddress([]byte{0x50})
	rm.ProposeAddValidator(newAddr, newSK.PublicKey())
	rm.MarkCommitted()

	// Apply at epoch boundary
	newSet := rm.ApplyAtEpochBoundary()
	if newSet == nil {
		t.Fatal("expected new set")
	}

	// Verify EpochManager has staged set
	if !em.HasStagedNext() {
		t.Fatal("expected staged next set")
	}

	// Advance epoch
	if !em.AdvanceEpoch() {
		t.Fatal("expected epoch to advance")
	}

	// Verify new set is active
	if em.CurrentEpoch() != 1 {
		t.Fatalf("expected epoch 1, got %d", em.CurrentEpoch())
	}
	if em.CurrentValidatorSet().Len() != 5 {
		t.Fatalf("expected 5 validators after epoch advance, got %d", em.CurrentValidatorSet().Len())
	}

	// Historical set should be available
	oldSet := em.ValidatorSetForView(1) // view 1 is in epoch 0
	if oldSet.Len() != 4 {
		t.Fatalf("expected 4 validators in historical epoch 0, got %d", oldSet.Len())
	}
}

func TestReconfigNilPublicKeyRejected(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	err := rm.ProposeAddValidator(types.BytesToAddress([]byte{0x60}), nil)
	if err == nil {
		t.Fatal("expected error for nil public key")
	}
}
