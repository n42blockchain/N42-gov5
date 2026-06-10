// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package blspool

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

func handoverPool(t *testing.T, allow bool) *Pool {
	t.Helper()
	var seed [32]byte
	seed[0] = 0x7e
	pool, err := NewSimulatedPool(PoolConfig{
		Seed: seed, PoolSize: 8, CommitteeSize: 4, RampBlocks: 0, AllowHandover: allow,
	})
	if err != nil {
		t.Fatalf("NewSimulatedPool: %v", err)
	}
	return pool
}

// realValidator derives a fresh keypair to stand in for a real validator (a
// different seed than the pool's).
func realValidator(t *testing.T) (common.SecretKey, common.PublicKey) {
	t.Helper()
	var seed [32]byte
	seed[0] = 0xab
	seed[31] = 0x01
	sks, pks, err := DeriveKeys(seed, 1, true)
	if err != nil {
		t.Fatalf("DeriveKeys: %v", err)
	}
	return sks[0], pks[0]
}

// TestHandoverFullCycle: register a real validator into a committee slot, submit
// its per-block signature, and confirm the evidence covers the whole committee
// and verifies.
func TestHandoverFullCycle(t *testing.T) {
	pool := handoverPool(t, true)
	const blockNum = uint64(20)
	blockHash := types.Hash{0x33}
	receiptRoot := types.Hash{0x44}

	// Pick a committee member to hand over.
	committee := pool.CommitteeAt(blockNum, blockHash)
	slot := committee[0]

	sk, pk := realValidator(t)

	// Register with a valid ownership proof.
	proof := sk.Sign(RegistrationMessage(slot, pk.Marshal()))
	if err := pool.RegisterValidator(slot, pk.Marshal(), proof.Marshal()); err != nil {
		t.Fatalf("RegisterValidator: %v", err)
	}
	if !pool.Replaced(slot) {
		t.Fatal("slot not marked replaced after registration")
	}

	// Before the real signature arrives, the replaced slot is uncovered.
	ce, err := pool.BuildBlockEvidence(blockNum, blockHash, receiptRoot)
	if err != nil {
		t.Fatalf("BuildBlockEvidence (pre-submit): %v", err)
	}
	covered, ok, err := pool.VerifyCE(ce)
	if err != nil {
		t.Fatalf("VerifyCE (pre-submit): %v", err)
	}
	if !ok {
		t.Fatal("VerifyCE rejected the partial-coverage evidence")
	}
	if covered != len(committee)-1 {
		t.Fatalf("covered = %d, want %d (one slot handed over, unsigned)", covered, len(committee)-1)
	}

	// Submit the validator's real signature for the block.
	realSig := sk.Sign(SigningMessage(blockNum, blockHash))
	if err := pool.SubmitSignature(blockNum, blockHash, slot, realSig.Marshal()); err != nil {
		t.Fatalf("SubmitSignature: %v", err)
	}

	// Now the evidence covers the full committee.
	ce2, err := pool.BuildBlockEvidence(blockNum, blockHash, receiptRoot)
	if err != nil {
		t.Fatalf("BuildBlockEvidence (post-submit): %v", err)
	}
	covered2, ok2, err := pool.VerifyCE(ce2)
	if err != nil {
		t.Fatalf("VerifyCE (post-submit): %v", err)
	}
	if !ok2 || covered2 != len(committee) {
		t.Fatalf("post-submit covered=%d ok=%v, want %d true", covered2, ok2, len(committee))
	}
}

// TestRegisterRejectsBadProof: a proof over the wrong message is rejected, and
// the slot stays simulated.
func TestRegisterRejectsBadProof(t *testing.T) {
	pool := handoverPool(t, true)
	sk, pk := realValidator(t)
	slot := 2

	// Proof over a DIFFERENT slot — must not authorize slot 2.
	badProof := sk.Sign(RegistrationMessage(slot+1, pk.Marshal()))
	if err := pool.RegisterValidator(slot, pk.Marshal(), badProof.Marshal()); err == nil {
		t.Fatal("RegisterValidator accepted a proof over the wrong slot")
	}
	if pool.Replaced(slot) {
		t.Fatal("slot was handed over despite invalid proof")
	}
}

// TestHandoverGateOff: with AllowHandover false, register/submit are refused.
func TestHandoverGateOff(t *testing.T) {
	pool := handoverPool(t, false)
	sk, pk := realValidator(t)
	proof := sk.Sign(RegistrationMessage(1, pk.Marshal()))
	if err := pool.RegisterValidator(1, pk.Marshal(), proof.Marshal()); err == nil {
		t.Fatal("RegisterValidator succeeded with hand-over disabled")
	}
	if err := pool.SubmitSignature(1, types.Hash{0x01}, 1, proof.Marshal()); err == nil {
		t.Fatal("SubmitSignature succeeded with hand-over disabled")
	}
}

// TestSubmitRejectsNonCommittee: a signature for a slot outside the block's
// committee is refused.
func TestSubmitRejectsNonCommittee(t *testing.T) {
	pool := handoverPool(t, true)
	const blockNum = uint64(5)
	blockHash := types.Hash{0x09}

	committee := pool.CommitteeAt(blockNum, blockHash)
	inCommittee := make(map[int]bool)
	for _, m := range committee {
		inCommittee[m] = true
	}
	// Find a slot NOT in the committee.
	outsider := -1
	for s := 0; s < 8; s++ {
		if !inCommittee[s] {
			outsider = s
			break
		}
	}
	if outsider < 0 {
		t.Skip("no non-committee slot available")
	}
	sk, pk := realValidator(t)
	proof := sk.Sign(RegistrationMessage(outsider, pk.Marshal()))
	if err := pool.RegisterValidator(outsider, pk.Marshal(), proof.Marshal()); err != nil {
		t.Fatalf("RegisterValidator: %v", err)
	}
	sig := sk.Sign(SigningMessage(blockNum, blockHash))
	if err := pool.SubmitSignature(blockNum, blockHash, outsider, sig.Marshal()); err == nil {
		t.Fatal("SubmitSignature accepted a non-committee slot")
	}
}

// TestBuildBlockEvidenceMatchesSimulated: with no hand-overs, BuildBlockEvidence
// equals BuildSimulatedCE byte-for-byte (safe drop-in).
func TestBuildBlockEvidenceMatchesSimulated(t *testing.T) {
	pool := handoverPool(t, true)
	const blockNum = uint64(11)
	blockHash := types.Hash{0x55}
	receiptRoot := types.Hash{0x66}

	a, err := pool.BuildSimulatedCE(blockNum, blockHash, receiptRoot)
	if err != nil {
		t.Fatalf("BuildSimulatedCE: %v", err)
	}
	b, err := pool.BuildBlockEvidence(blockNum, blockHash, receiptRoot)
	if err != nil {
		t.Fatalf("BuildBlockEvidence: %v", err)
	}
	if string(a.Marshal()) != string(b.Marshal()) {
		t.Fatal("BuildBlockEvidence diverged from BuildSimulatedCE with no hand-overs")
	}
}
