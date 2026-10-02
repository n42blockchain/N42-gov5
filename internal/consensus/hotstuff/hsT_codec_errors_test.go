// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestCodecDecode_MalformedBytesError covers the UnmarshalSSZ error branch of
// each decode function with a deliberately truncated/garbage payload.
func TestCodecDecode_MalformedBytesError(t *testing.T) {
	garbage := []byte{0x01, 0x02, 0x03}

	if _, err := decodeTimeoutMsg(garbage); err == nil {
		t.Error("decodeTimeoutMsg(garbage) = nil error, want one")
	}
	if _, err := decodeNewViewMsg(garbage); err == nil {
		t.Error("decodeNewViewMsg(garbage) = nil error, want one")
	}
	if _, err := decodeVote(garbage); err == nil {
		t.Error("decodeVote(garbage) = nil error, want one")
	}
	if _, err := decodeCommitVote(garbage); err == nil {
		t.Error("decodeCommitVote(garbage) = nil error, want one")
	}
	if _, err := decodePrepareQCMsg(garbage); err == nil {
		t.Error("decodePrepareQCMsg(garbage) = nil error, want one")
	}
	if _, err := decodeProposal(garbage); err == nil {
		t.Error("decodeProposal(garbage) = nil error, want one")
	}
	if _, err := decodeDecide(garbage); err == nil {
		t.Error("decodeDecide(garbage) = nil error, want one")
	}
	if _, err := decodeTC(garbage); err == nil {
		t.Error("decodeTC(garbage) = nil error, want one")
	}
	if _, err := DecodeConsensusMsg(garbage); err == nil {
		t.Error("DecodeConsensusMsg(garbage) = nil error, want one")
	}
}

// TestDecodeH2V4Gossip_InvalidSnappyPayload covers the invalid-snappy-length
// early return.
func TestDecodeH2V4Gossip_InvalidSnappyPayload(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 1}
	if _, err := DecodeH2V4Gossip([]byte{0xff, 0xff, 0xff, 0xff, 0xff}, identity); err == nil {
		t.Error("DecodeH2V4Gossip(invalid snappy) = nil error, want one")
	}
}

// TestNotifyBlockSealed_EngineBranches covers both the engine-present and
// engine-absent paths.
func TestNotifyBlockSealed_EngineBranches(t *testing.T) {
	// Engine absent: InitEngine never called.
	h := New(nil, nil)
	h.NotifyBlockSealed(types.Hash{0x01}, types.Hash{0x02}) // must not panic

	// Engine present.
	setup := newTestSetup(t, 4)
	h2 := New(nil, nil)
	h2.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h2.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	h2.NotifyBlockSealed(types.Hash{0x03}, types.Hash{0x04})
}

// TestWaitForCommitVoteJournal_EngineBranches covers both the engine-present
// (delegates to the engine) and engine-absent (returns the "off" sentinel)
// paths.
func TestWaitForCommitVoteJournal_EngineBranches(t *testing.T) {
	h := New(nil, nil)
	d, reason := h.WaitForCommitVoteJournal(types.Hash{0x01}, time.Second)
	if d != 0 || reason != "off" {
		t.Fatalf("WaitForCommitVoteJournal(no engine) = (%v, %q), want (0, \"off\")", d, reason)
	}

	setup := newTestSetup(t, 4)
	h2 := New(nil, nil)
	h2.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h2.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	// With the engine present but the feature flag unset, this still returns
	// promptly (a no-op fast path inside the engine); just exercise the
	// delegation branch for coverage, bounded well under the test harness's
	// deadline.
	h2.WaitForCommitVoteJournal(types.Hash{0x02}, 50*time.Millisecond)
}
