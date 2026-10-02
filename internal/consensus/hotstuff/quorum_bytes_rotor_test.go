package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// TestVoteCollector_AddVoteFromBytes covers the raw-bytes vote entry point,
// both the malformed-signature error and the success delegation to AddVote.
func TestVoteCollector_AddVoteFromBytes(t *testing.T) {
	vc := NewVoteCollector(1, types.Hash{0x01}, 4)

	if err := vc.AddVoteFromBytes(0, []byte("not-a-signature")); err == nil {
		t.Fatalf("expected error for malformed signature bytes")
	}

	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	sig := sk.Sign([]byte("msg")).Marshal()
	if err := vc.AddVoteFromBytes(0, sig); err != nil {
		t.Fatalf("AddVoteFromBytes: unexpected error: %v", err)
	}
	// Duplicate from the same validator index is rejected.
	if err := vc.AddVoteFromBytes(0, sig); err == nil {
		t.Fatalf("expected duplicate-vote error")
	}
}

// TestTimeoutCollector_FromBytesVariants covers AddTimeoutFromBytes and
// AddVerifiedTimeoutFromBytes, including malformed-signature rejection and
// the verified-flag side effect.
func TestTimeoutCollector_FromBytesVariants(t *testing.T) {
	tcoll := NewTimeoutCollector(2, 4)
	highQC := QuorumCertificate{View: 1}

	if err := tcoll.AddTimeoutFromBytes(0, []byte("garbage"), highQC); err == nil {
		t.Fatalf("expected error for malformed signature bytes")
	}

	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	sig := sk.Sign([]byte("timeout")).Marshal()
	if err := tcoll.AddTimeoutFromBytes(0, sig, highQC); err != nil {
		t.Fatalf("AddTimeoutFromBytes: unexpected error: %v", err)
	}

	if err := tcoll.AddVerifiedTimeoutFromBytes(1, []byte("garbage"), highQC); err == nil {
		t.Fatalf("expected error for malformed verified signature bytes")
	}
	sk2, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	sig2 := sk2.Sign([]byte("timeout2")).Marshal()
	if err := tcoll.AddVerifiedTimeoutFromBytes(1, sig2, highQC); err != nil {
		t.Fatalf("AddVerifiedTimeoutFromBytes: unexpected error: %v", err)
	}
	if tcoll.TimeoutCount() != 2 {
		t.Fatalf("expected 2 timeouts collected, got %d", tcoll.TimeoutCount())
	}
}

// TestRotor_VoteFallbackStats covers the vote direct/fallback counters.
func TestRotor_VoteFallbackStats(t *testing.T) {
	r := NewRotor(2)
	r.RecordVoteDirect()
	r.RecordVoteDirect()
	r.RecordVoteFallback()

	direct, fallback := r.VoteStats()
	if direct != 2 || fallback != 1 {
		t.Fatalf("expected direct=2 fallback=1, got direct=%d fallback=%d", direct, fallback)
	}
}
