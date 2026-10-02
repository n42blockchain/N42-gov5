// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestBuildTCWithMessages_InsufficientTimeouts covers the quorum-size guard
// before any signature work happens.
func TestBuildTCWithMessages_InsufficientTimeouts(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(1, uint32(setup.vs.Len()))
	msg := TimeoutSigningMessage(1)
	for i := 0; i < 2; i++ { // quorum is 3
		sig := setup.keys[i].Sign(msg)
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, GenesisQC()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tc.BuildTCWithMessages(setup.vs, msg, func(*QuorumCertificate) error { return nil }); err == nil {
		t.Fatal("expected an insufficient-votes error")
	}
}

// TestBuildTCWithMessages_SelectsHighestQCAndVerifies covers the normal
// success path: the highest-view embedded QC is selected and passed to the
// verification callback.
func TestBuildTCWithMessages_SelectsHighestQCAndVerifies(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(1, uint32(setup.vs.Len()))
	msg := TimeoutSigningMessage(1)

	highQCs := []QuorumCertificate{
		{View: 1, BlockHash: types.Hash{0x01}, Signers: []bool{true}},
		{View: 3, BlockHash: types.Hash{0x03}, Signers: []bool{true}}, // highest
		{View: 2, BlockHash: types.Hash{0x02}, Signers: []bool{true}},
	}
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(msg)
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, highQCs[i]); err != nil {
			t.Fatal(err)
		}
	}

	var verifiedView ViewNumber
	result, err := tc.BuildTCWithMessages(setup.vs, msg, func(qc *QuorumCertificate) error {
		verifiedView = qc.View
		return nil
	})
	if err != nil {
		t.Fatalf("BuildTCWithMessages: %v", err)
	}
	if result.HighQC.View != 3 {
		t.Fatalf("selected highQC.View = %d, want 3 (the highest)", result.HighQC.View)
	}
	if verifiedView != 3 {
		t.Fatalf("verifyHighQC was called with view %d, want 3", verifiedView)
	}
}

// TestBuildTCWithMessages_FallsBackToGenesisOnVerifyFailure covers the
// defensive fallback: a highQC that fails verification is replaced with the
// genesis QC rather than failing TC formation outright.
func TestBuildTCWithMessages_FallsBackToGenesisOnVerifyFailure(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(1, uint32(setup.vs.Len()))
	msg := TimeoutSigningMessage(1)
	highQC := QuorumCertificate{View: 5, BlockHash: types.Hash{0x05}, Signers: []bool{true, true, true}}

	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(msg)
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, highQC); err != nil {
			t.Fatal(err)
		}
	}

	result, err := tc.BuildTCWithMessages(setup.vs, msg, func(*QuorumCertificate) error {
		return errors.New("forced verification failure")
	})
	if err != nil {
		t.Fatalf("BuildTCWithMessages: %v", err)
	}
	genesis := GenesisQC()
	if result.HighQC.View != genesis.View || result.HighQC.BlockHash != genesis.BlockHash {
		t.Fatalf("expected a genesis-QC fallback, got %+v", result.HighQC)
	}
}

// TestBuildTCWithMessages_UnverifiedBadSignatureIsSkipped covers the
// on-the-fly signature verification branch (AddTimeout, not
// AddVerifiedTimeout): an entry with a signature that does not match the
// expected message is excluded from the quorum count.
func TestBuildTCWithMessages_UnverifiedBadSignatureIsSkipped(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(1, uint32(setup.vs.Len()))
	msg := TimeoutSigningMessage(1)

	// Three good, unverified signatures (quorum).
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(msg)
		if err := tc.AddTimeout(ValidatorIndex(i), sig, GenesisQC()); err != nil {
			t.Fatal(err)
		}
	}
	// A fourth, badly-signed entry (signs the wrong message) must not count.
	bad := setup.keys[3].Sign(TimeoutSigningMessage(99))
	if err := tc.AddTimeout(3, bad, GenesisQC()); err != nil {
		t.Fatal(err)
	}

	result, err := tc.BuildTCWithMessages(setup.vs, msg, func(*QuorumCertificate) error { return nil })
	if err != nil {
		t.Fatalf("BuildTCWithMessages: %v", err)
	}
	if result.Signers[3] {
		t.Fatal("the badly-signed entry must not be counted as a signer")
	}
}

// TestService_PublishH2V4Decide covers the H2-v4 interop envelope encode and
// gossip publish path over a real mocknet transport.
func TestService_PublishH2V4Decide(t *testing.T) {
	p0, _ := hsTMocknetPair(t)
	identity := H2V4ChainIdentity{ChainID: 7}
	s := &Service{
		p2p:          p0,
		ctx:          context.Background(),
		h2V4Identity: &identity,
	}

	decide := &Decide{View: 1, BlockHash: types.Hash{0x0a}, CommitQC: GenesisQC()}
	msg := &ConsensusMsg{Type: MsgDecide, Payload: decide}

	done := make(chan struct{})
	go func() {
		s.publishH2V4Decide(msg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publishH2V4Decide did not return in time")
	}
}
