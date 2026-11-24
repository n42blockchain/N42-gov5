package hotstuff

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/params"
)

// TestProfile1FixedValidatorSet: on a pinned (Profile-1) set the
// reconfiguration API refuses every change and nothing is ever staged
// (standard v0.2.1 AM5).
func TestProfile1FixedValidatorSet(t *testing.T) {
	validators := makeTestValidators(5)
	em := NewEpochManagerWithLength(NewValidatorSet(validators, 1), 10)
	em.SetFixedSet()
	rm := NewReconfigurationManager(em)

	sk, _ := blst.RandKey()
	if err := rm.ProposeAddValidator(types.BytesToAddress([]byte{0x10}), sk.PublicKey()); !errors.Is(err, errFixedValidatorSet) {
		t.Fatalf("ProposeAddValidator on a fixed set: %v", err)
	}
	if err := rm.ProposeRemoveValidator(validators[0].Address); !errors.Is(err, errFixedValidatorSet) {
		t.Fatalf("ProposeRemoveValidator on a fixed set: %v", err)
	}
	rm.MarkCommitted()
	if rm.HasPendingChanges() || rm.IsCommitted() || rm.ApplyAtEpochBoundary() != nil {
		t.Fatal("a fixed set has pending or committed changes")
	}

	// No other source can stage a set either (restore, schedule).
	em.StageNextEpoch(validators[:4], 1)
	if em.HasStagedNext() {
		t.Fatal("a fixed set accepted a staged change")
	}
}

// TestProfile1AdapterPinsGenesisSet: InitEngine pins the set on a Profile-1
// chain and leaves a legacy chain reconfigurable.
func TestProfile1AdapterPinsGenesisSet(t *testing.T) {
	for _, tc := range []struct {
		profile uint64
		fixed   bool
	}{{0, false}, {1, true}} {
		validators := makeTestValidators(4)
		sk, _ := blst.RandKey()
		validators[0].PublicKey = sk.PublicKey()
		hs := &params.HotStuffConfig{EpochLength: 10, BaseTimeout: 1000, MaxTimeout: 2000, BaseTimeoutMs: 1000, MaxTimeoutMs: 2000}
		h := New(hs, &params.ChainConfig{N42Profile: tc.profile, HotStuff: hs})
		h.Authorize(validators[0].Address, sk)
		if err := h.InitEngine(validators, 1); err != nil {
			t.Fatal(err)
		}
		if got := h.epochManager.FixedSet(); got != tc.fixed {
			t.Fatalf("profile %d: fixed set = %v, want %v", tc.profile, got, tc.fixed)
		}
	}
}
