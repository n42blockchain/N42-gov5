package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestNewRotor_DefaultRelayCount covers the <=0 default-to-3 branch.
func TestNewRotor_DefaultRelayCount(t *testing.T) {
	r := NewRotor(0)
	if r.relayCount != 3 {
		t.Fatalf("expected default relayCount=3, got %d", r.relayCount)
	}
	r2 := NewRotor(-5)
	if r2.relayCount != 3 {
		t.Fatalf("expected default relayCount=3 for negative input, got %d", r2.relayCount)
	}
	r3 := NewRotor(7)
	if r3.relayCount != 7 {
		t.Fatalf("expected relayCount=7, got %d", r3.relayCount)
	}
}

// TestRoundState_VotedHashInView covers both the "voted" and "never voted /
// wrong view / view zero" branches.
func TestRoundState_VotedHashInView(t *testing.T) {
	rs := &RoundState{}
	if _, ok := rs.VotedHashInView(ViewNumber(1)); ok {
		t.Fatalf("expected false before any vote recorded")
	}

	rs.RecordVote(ViewNumber(5), types.Hash{0x09})
	if hash, ok := rs.VotedHashInView(ViewNumber(5)); !ok || hash != (types.Hash{0x09}) {
		t.Fatalf("expected recorded vote hash, got ok=%v hash=%x", ok, hash)
	}
	if _, ok := rs.VotedHashInView(ViewNumber(6)); ok {
		t.Fatalf("expected false for a different view")
	}

	// view==0 never counts as voted, even if votedInView happens to be 0.
	rsZero := &RoundState{}
	if _, ok := rsZero.VotedHashInView(ViewNumber(0)); ok {
		t.Fatalf("expected false for view 0")
	}
}

// TestLeaderForView_EmptySet covers the IsEmpty guard returning index 0.
func TestLeaderForView_EmptySet(t *testing.T) {
	empty := NewValidatorSet(nil, 0)
	if idx := LeaderForView(ViewNumber(5), empty); idx != 0 {
		t.Fatalf("expected leader index 0 for an empty validator set, got %d", idx)
	}
	if IsLeader(ValidatorIndex(0), ViewNumber(5), empty) != true {
		t.Fatalf("expected index 0 to be considered leader for an empty set")
	}
}
