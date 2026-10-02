// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eth_light_client_cov_test.go exercises EthLightClient's ProcessUpdate
// state machine, header hashing and accessor methods using a fake BLS
// verifier (no real BLS12-381 signatures, no network).

package bridge

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// computeMerkleRootFromBranch mirrors verifyMerkleBranch's hashing order to
// deterministically compute the root a given (leaf, branch, index) implies.
func computeMerkleRootFromBranch(leaf types.Hash, branch []types.Hash, index int) types.Hash {
	value := leaf
	var buf [64]byte
	for i := 0; i < len(branch); i++ {
		if (index>>i)&1 == 1 {
			copy(buf[:32], branch[i][:])
			copy(buf[32:], value[:])
		} else {
			copy(buf[:32], value[:])
			copy(buf[32:], branch[i][:])
		}
		value = types.Hash(sha256.Sum256(buf[:]))
	}
	return value
}

// covFakeBLSVerifier is an in-memory SyncCommitteeBLSVerifier fake.
type covFakeBLSVerifier struct {
	err      error
	calls    int
	lastKeys int
}

func (f *covFakeBLSVerifier) VerifySyncCommitteeSignature(pubKeys []blscommon.PublicKey, _ []byte, _ [32]byte) error {
	f.calls++
	f.lastKeys = len(pubKeys)
	return f.err
}

func fullParticipationBits() [SyncCommitteeSize / 8]byte {
	var bits [SyncCommitteeSize / 8]byte
	for i := range bits {
		bits[i] = 0xFF
	}
	return bits
}

func newTestCommittee() *SyncCommittee {
	return &SyncCommittee{}
}

func TestEthHeader_HashAndSyncPeriod(t *testing.T) {
	h := &EthHeader{Slot: SlotsPerSyncPeriod*2 + 5, ProposerIndex: 1, StateRoot: types.Hash{0x1}}
	if h.SyncPeriod() != 2 {
		t.Fatalf("SyncPeriod = %d, want 2", h.SyncPeriod())
	}
	h1 := h.Hash()
	h.ProposerIndex = 2
	h2 := h.Hash()
	if h1 == h2 {
		t.Fatal("Hash should change when a field changes")
	}
}

func TestSyncAggregate_ParticipantCount(t *testing.T) {
	sa := &SyncAggregate{}
	if sa.ParticipantCount() != 0 {
		t.Fatalf("empty bitmap should have 0 participants")
	}
	sa.SyncCommitteeBits = fullParticipationBits()
	if sa.ParticipantCount() != SyncCommitteeSize {
		t.Fatalf("full bitmap participants = %d, want %d", sa.ParticipantCount(), SyncCommitteeSize)
	}
}

func TestNewEthLightClient_Validation(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	if _, err := NewEthLightClient(nil, verifier); err == nil {
		t.Fatal("expected error for nil config")
	}
	if _, err := NewEthLightClient(&EthLightClientConfig{}, verifier); err == nil {
		t.Fatal("expected error for missing initial committee")
	}
	if _, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: newTestCommittee()}, nil); err == nil {
		t.Fatal("expected error for nil verifier")
	}

	lc, err := NewEthLightClient(&EthLightClientConfig{
		InitialCommittee: newTestCommittee(),
		InitialHeader:    &EthHeader{Slot: 10, StateRoot: types.Hash{0x2}},
	}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}
	if !lc.IsSlotVerified(10) {
		t.Fatal("initial header slot should be verified")
	}
	if root, ok := lc.GetVerifiedStateRoot(10); !ok || root != (types.Hash{0x2}) {
		t.Fatalf("GetVerifiedStateRoot = %x, %v", root, ok)
	}
	if lc.CurrentPeriod() != 0 {
		t.Fatalf("CurrentPeriod = %d, want 0", lc.CurrentPeriod())
	}
}

func TestEthLightClient_ProcessUpdate_Validation(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: newTestCommittee()}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	if err := lc.ProcessUpdate(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil update")
	}
	if err := lc.ProcessUpdate(context.Background(), &SyncCommitteeUpdate{}); err == nil {
		t.Fatal("expected error for incomplete update")
	}
	if err := lc.ProcessUpdate(context.Background(), &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: 1},
		FinalizedHeader: &EthHeader{Slot: 1},
	}); err == nil {
		t.Fatal("expected error for missing sync aggregate")
	}

	lowParticipation := &SyncAggregate{} // zero bits => 0 participants
	if err := lc.ProcessUpdate(context.Background(), &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: 1},
		FinalizedHeader: &EthHeader{Slot: 1},
		SyncAggregate:   lowParticipation,
	}); err == nil {
		t.Fatal("expected error for insufficient participants")
	}
}

// committeeWithValidKeys builds a SyncCommittee whose 512 slots all hold the
// same validly-encoded (but not cryptographically meaningful for this test,
// since BLS verification itself is faked) compressed BLS12-381 public key,
// so extractParticipantKeys succeeds and execution reaches later checks.
func committeeWithValidKeys(t *testing.T) *SyncCommittee {
	t.Helper()
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("bls.RandKey: %v", err)
	}
	pubBytes := sk.PublicKey().Marshal()
	c := &SyncCommittee{AggregatePubKey: pubBytes}
	for i := range c.PubKeys {
		c.PubKeys[i] = pubBytes
	}
	return c
}

func TestEthLightClient_ProcessUpdate_RequiresFinalityBranch(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: committeeWithValidKeys(t)}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	update := &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: 1},
		FinalizedHeader: &EthHeader{Slot: 1},
		SyncAggregate:   &SyncAggregate{SyncCommitteeBits: fullParticipationBits()},
	}
	err = lc.ProcessUpdate(context.Background(), update)
	if err == nil || err.Error() != "finality branch required" {
		t.Fatalf("expected finality branch required error, got %v", err)
	}
	if verifier.calls != 1 {
		t.Fatalf("expected BLS verifier to be called once, got %d", verifier.calls)
	}
	if verifier.lastKeys != SyncCommitteeSize {
		t.Fatalf("expected all %d participants extracted, got %d", SyncCommitteeSize, verifier.lastKeys)
	}
}

func TestEthLightClient_ProcessUpdate_SuccessWithFinalityBranch(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: committeeWithValidKeys(t)}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	finalized := &EthHeader{Slot: 1, StateRoot: types.Hash{0x3}}
	attested := &EthHeader{Slot: 2}

	// Build a finality branch that verifies: start from the finalized root
	// and walk up depth=6 using zero siblings, then force attested.StateRoot
	// to the resulting root so verifyMerkleBranch succeeds deterministically.
	branch := make([]types.Hash, 6)
	root := computeMerkleRootFromBranch(finalized.Hash(), branch, 41)
	attested.StateRoot = root

	update := &SyncCommitteeUpdate{
		AttestedHeader:  attested,
		FinalizedHeader: finalized,
		SyncAggregate:   &SyncAggregate{SyncCommitteeBits: fullParticipationBits()},
		FinalityBranch:  branch,
	}
	if err := lc.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate: %v", err)
	}
	if lc.LatestFinalized().Slot != 1 {
		t.Fatalf("LatestFinalized slot = %d, want 1", lc.LatestFinalized().Slot)
	}
	if !lc.IsSlotVerified(1) {
		t.Fatal("expected slot 1 to be verified after update")
	}
}

func TestEthLightClient_ProcessUpdate_BLSFailure(t *testing.T) {
	verifier := &covFakeBLSVerifier{err: context.DeadlineExceeded}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: committeeWithValidKeys(t)}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	update := &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: 1},
		FinalizedHeader: &EthHeader{Slot: 1},
		SyncAggregate:   &SyncAggregate{SyncCommitteeBits: fullParticipationBits()},
		FinalityBranch:  make([]types.Hash, 6),
	}
	if err := lc.ProcessUpdate(context.Background(), update); err == nil {
		t.Fatal("expected BLS verification failure to propagate")
	}
}

func TestEthLightClient_ProcessUpdate_StaleAttestedSlot(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{
		InitialCommittee: newTestCommittee(),
		InitialHeader:    &EthHeader{Slot: 100},
	}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	update := &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: 50},
		FinalizedHeader: &EthHeader{Slot: 50},
		SyncAggregate:   &SyncAggregate{SyncCommitteeBits: fullParticipationBits()},
	}
	if err := lc.ProcessUpdate(context.Background(), update); err == nil {
		t.Fatal("expected error for stale attested slot")
	}
}

func TestEthLightClient_ProcessUpdate_WrongPeriod(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: newTestCommittee()}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	// Attested header is 3 periods ahead of current (0) -> rejected.
	update := &SyncCommitteeUpdate{
		AttestedHeader:  &EthHeader{Slot: SlotsPerSyncPeriod * 3},
		FinalizedHeader: &EthHeader{Slot: SlotsPerSyncPeriod * 3},
		SyncAggregate:   &SyncAggregate{SyncCommitteeBits: fullParticipationBits()},
	}
	if err := lc.ProcessUpdate(context.Background(), update); err == nil {
		t.Fatal("expected error for attested period outside [current, current+1]")
	}
}

func TestEthLightClient_LatestFinalizedNilByDefault(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{InitialCommittee: newTestCommittee()}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}
	if lc.LatestFinalized() != nil {
		t.Fatal("expected nil LatestFinalized with no InitialHeader")
	}
	if lc.IsSlotVerified(1) {
		t.Fatal("slot 1 should not be verified")
	}
	if _, ok := lc.GetVerifiedStateRoot(1); ok {
		t.Fatal("expected no verified state root for slot 1")
	}
}
