package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/blst"
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
	// n=5, f=1: the safe quorum is n-f=4, NOT the old 2f+1=3. Two size-3
	// quorums in a 5-set can intersect in only 2*3-5=1 node, which could be
	// the single Byzantine one — so 3 admitted two conflicting commits.
	if result.QuorumSize() != 4 {
		t.Fatalf("expected quorum=4 (n-f) for n=5, got %d", result.QuorumSize())
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
	if !em.AdvanceEpoch(11) {
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

func TestReconfigApplyWithoutCommit(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	rm.ProposeAddValidator(types.BytesToAddress([]byte{0x70}), sk.PublicKey())
	// Do NOT call MarkCommitted

	result := rm.ApplyAtEpochBoundary()
	if result != nil {
		t.Fatal("should not apply uncommitted changes")
	}
}

func TestReconfigApplyWithoutEpochs(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManager(vs) // epochs disabled (length=0)
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	rm.ProposeAddValidator(types.BytesToAddress([]byte{0x71}), sk.PublicKey())
	rm.MarkCommitted()

	result := rm.ApplyAtEpochBoundary()
	if result != nil {
		t.Fatal("should not apply when epochs are disabled")
	}
}

func TestReconfigPendingCounts(t *testing.T) {
	validators := makeTestValidators(7)
	vs := NewValidatorSet(validators, 2)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	if rm.PendingAddCount() != 0 || rm.PendingRemoveCount() != 0 {
		t.Fatal("fresh manager should have zero counts")
	}

	sk, _ := blst.RandKey()
	rm.ProposeAddValidator(types.BytesToAddress([]byte{0x72}), sk.PublicKey())
	if rm.PendingAddCount() != 1 {
		t.Errorf("PendingAddCount = %d, want 1", rm.PendingAddCount())
	}

	rm.ProposeRemoveValidator(validators[0].Address)
	if rm.PendingRemoveCount() != 1 {
		t.Errorf("PendingRemoveCount = %d, want 1", rm.PendingRemoveCount())
	}
}

func TestReconfigDoubleRemoveRejected(t *testing.T) {
	validators := makeTestValidators(7)
	vs := NewValidatorSet(validators, 2)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	err := rm.ProposeRemoveValidator(validators[0].Address)
	if err != nil {
		t.Fatalf("first remove should succeed: %v", err)
	}
	err = rm.ProposeRemoveValidator(validators[0].Address)
	if err == nil {
		t.Fatal("duplicate remove should fail")
	}
}

func TestReconfigMarkCommittedIdempotent(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	rm.ProposeAddValidator(types.BytesToAddress([]byte{0x73}), sk.PublicKey())
	rm.MarkCommitted()
	rm.MarkCommitted() // idempotent
	if !rm.IsCommitted() {
		t.Fatal("should still be committed after double MarkCommitted")
	}
}

func TestReconfigRemoveNonexistent(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	err := rm.ProposeRemoveValidator(types.BytesToAddress([]byte{0xFF}))
	if err == nil {
		t.Fatal("removing nonexistent validator should fail")
	}
}

func TestReconfigDoubleAddRejected(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	addr := types.BytesToAddress([]byte{0x74})
	err := rm.ProposeAddValidator(addr, sk.PublicKey())
	if err != nil {
		t.Fatalf("first add should succeed: %v", err)
	}
	err = rm.ProposeAddValidator(addr, sk.PublicKey())
	if err == nil {
		t.Fatal("duplicate add should fail")
	}
}

// TestSeedCurrentEpochAlignsHistoricalSets verifies that after a mid-chain
// restart, seeding the epoch counter from the recovered head view keeps
// historicalSets keys (written by AdvanceEpoch under currentEpoch) aligned with
// the EpochForView lookups in ValidatorSetForView. Without the seed, currentEpoch
// stays at 0, historicalSets[0] is written, but ValidatorSetForView(oldView)
// queries historicalSets[EpochForView(oldView)] and silently falls back to the
// new set — returning the wrong validators for a historical view.
func TestSeedCurrentEpochAlignsHistoricalSets(t *testing.T) {
	const epochLen = uint64(10)
	oldValidators := makeTestValidators(4)
	oldSet := NewValidatorSet(oldValidators, 1)
	em := NewEpochManagerWithLength(oldSet, epochLen)

	// Simulate a mid-chain restart at head view 1000 → epoch 99.
	const headView = uint64(1000)
	em.SeedCurrentEpoch(headView)
	wantEpoch := em.EpochForView(headView) // (1000-1)/10 = 99
	if em.CurrentEpoch() != wantEpoch {
		t.Fatalf("seeded epoch = %d, want %d", em.CurrentEpoch(), wantEpoch)
	}

	// A view inside the seeded epoch (epoch 99 = views 991..1000) resolves to
	// the current (old) set.
	oldView := headView - 5 // 995, still epoch 99
	if got := em.ValidatorSetForView(oldView); got.Len() != oldSet.Len() {
		t.Fatalf("pre-transition set len = %d, want %d", got.Len(), oldSet.Len())
	}

	// Stage + activate a reconfig at the next boundary (view 1011 → epoch 100).
	em.StageNextEpoch(makeTestValidators(5), 1)
	if !em.AdvanceEpoch(1001) {
		t.Fatal("AdvanceEpoch should activate the staged set")
	}
	if em.CurrentEpoch() != wantEpoch+1 {
		t.Fatalf("post-advance epoch = %d, want %d", em.CurrentEpoch(), wantEpoch+1)
	}

	// The old epoch's view must still resolve to the OLD set via historicalSets,
	// keyed by EpochForView(oldView)=99 — which only matches because we seeded.
	if got := em.ValidatorSetForView(oldView); got.Len() != oldSet.Len() {
		t.Fatalf("historical set len = %d, want old %d (seed misaligned historicalSets)",
			got.Len(), oldSet.Len())
	}
	// A view in the new epoch (100 = views 1001..1010) resolves to the new set.
	newView := headView + 5 // 1005, epoch 100
	if got := em.ValidatorSetForView(newView); got.Len() != 5 {
		t.Fatalf("new-epoch set len = %d, want 5", got.Len())
	}
}

// TestMarkCommittedStagesForVerification is the core property behind cross-boundary
// catch-up: MarkCommitted (CommitQC time) stages the new set immediately, so it is
// resolvable for the next epoch's views BEFORE the boundary activates it. That lets a node
// verify post-boundary blocks (whose QCs carry the new, larger signer bitmap) while
// it is still on the old set — the chicken-and-egg that previously stalled catch-up.
func TestMarkCommittedStagesForVerification(t *testing.T) {
	validators := makeTestValidators(4) // n=4
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	rm.ProposeAddValidator(types.BytesToAddress([]byte{0x10}), sk.PublicKey())

	// Before commit: no staged set; the next epoch's view must be unresolved.
	if em.HasStagedNext() {
		t.Fatal("must not be staged before commit")
	}
	if em.ValidatorSetForView(11) != nil {
		t.Fatal("next epoch set must not resolve before commit")
	}

	rm.MarkCommitted() // stages at commit, NOT at the boundary

	// After commit, BEFORE any AdvanceEpoch: the size-5 set is staged and view-bound,
	// while the active set is still size 4.
	if !em.HasStagedNext() {
		t.Fatal("MarkCommitted must stage the next set")
	}
	if em.CurrentValidatorSet().Len() != 4 {
		t.Fatalf("active set must still be 4 before the boundary, got %d", em.CurrentValidatorSet().Len())
	}
	if got := em.ValidatorSetForView(11); got == nil || got.Len() != 5 {
		t.Fatal("staged size-5 set must resolve for the next epoch before the boundary")
	}

	// Activation at the boundary swaps it in.
	if !em.AdvanceEpoch(11) {
		t.Fatal("AdvanceEpoch should activate the staged set")
	}
	if em.CurrentValidatorSet().Len() != 5 {
		t.Fatalf("active set must be 5 after activation, got %d", em.CurrentValidatorSet().Len())
	}
}

// A BLS signature proves that a key set signed bytes; it does not prove that the
// set was authorized for the signed view. Exact epoch resolution must therefore
// reject an old set's bitmap at a future view and must not guess beyond a staged
// next epoch.
func TestResolveQCValidatorSetBindsAuthorityToView(t *testing.T) {
	oldSet := NewValidatorSet(makeTestValidators(4), 1)
	em := NewEpochManagerWithLength(oldSet, 10)
	em.StageNextEpoch(makeTestValidators(5), 1)
	sk, _ := blst.RandKey()
	engine := NewConsensusEngineWithEpochManager(0, sk, em, 1_000, 2_000, make(chan EngineOutput, 1))

	if got := engine.resolveQCValidatorSet(11, 4); got != nil {
		t.Fatal("old validator bitmap must not authorize a certificate in the next epoch")
	}
	if got := engine.resolveQCValidatorSet(11, 5); got != em.PeekNextSet() {
		t.Fatal("next-epoch view must resolve only to the staged next set")
	}
	if got := engine.resolveQCValidatorSet(21, 5); got != nil {
		t.Fatal("an unstaged future epoch must fail closed")
	}

	// Same-width rotations are equally security-sensitive: select by epoch, not
	// by bitmap length or by preferring the current set.
	sameWidth := NewEpochManagerWithLength(NewValidatorSet(makeTestValidators(4), 1), 10)
	sameWidth.StageNextEpoch(makeTestValidators(4), 1)
	engine = NewConsensusEngineWithEpochManager(0, sk, sameWidth, 1_000, 2_000, make(chan EngineOutput, 1))
	if got := engine.resolveQCValidatorSet(11, 4); got != sameWidth.PeekNextSet() {
		t.Fatal("same-width next epoch must resolve to its staged set, not the old set")
	}
}

func TestRestoreActiveSetDoesNotGuessPreviousAuthority(t *testing.T) {
	em := NewEpochManagerWithLength(NewValidatorSet(makeTestValidators(4), 1), 10)
	em.RestoreActiveSet(5, makeTestValidators(5), 1)

	if got := em.ValidatorSetForView(51); got == nil || got.Len() != 5 {
		t.Fatal("restored current epoch must resolve to its persisted set")
	}
	if got := em.ValidatorSetForView(41); got != nil {
		t.Fatal("startup validator set must not be guessed as the previous epoch authority")
	}
}
