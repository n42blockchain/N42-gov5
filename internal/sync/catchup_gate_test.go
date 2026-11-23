package sync

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

func gateTestPeers(t *testing.T, fp *fakeP2P, heights ...uint64) []peer.ID {
	t.Helper()
	ids := make([]peer.ID, 0, len(heights))
	for _, h := range heights {
		pid, err := test.RandPeerID()
		if err != nil {
			t.Fatalf("RandPeerID: %v", err)
		}
		fp.addConnectedPeer(t, pid, h)
		ids = append(ids, pid)
	}
	return ids
}

// A single peer advertising an absurd height must not count as "behind": a
// 7-validator network (f=2) needs three peers above the threshold.
func TestHeightBehindQuorumIgnoresSingleLyingPeer(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	gateTestPeers(t, fp, 1_000_100, 100, 101, 100, 99, 100)
	if got := svc.HeightBehindQuorum(3); got != 0 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 0 with one liar and honest peers at the head", got)
	}
	// The old gate is what stopped production.
	if got := svc.HeightBehind(); got != 1_000_000 {
		t.Fatalf("legacy HeightBehind() = %d, want 1000000", got)
	}
}

// f liars cannot raise the quorum height above an honest peer's height.
func TestHeightBehindQuorumUsesFPlusOneHighest(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	// two liars (f=2) and three honest peers ahead by 50, 60 and 70.
	gateTestPeers(t, fp, 9_000_000, 8_000_000, 150, 160, 170, 100)
	if got := svc.HeightBehindQuorum(3); got != 70 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 70 (third-highest advertised height)", got)
	}
	// Two honest peers alone are below the quorum.
	svc2, fp2, _ := newCatchUpTestService(t, 100)
	gateTestPeers(t, fp2, 150, 160, 100, 100)
	if got := svc2.HeightBehindQuorum(3); got != 0 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 0 with only two peers ahead", got)
	}
}

// A gap named by a verified CommitQC gates production with no peer claim.
func TestHeightBehindQuorumVerifiedCommitTarget(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	svc.catchUpInProgress.Store(true) // keep CatchUpTo off the network path
	svc.CatchUpTo(180)
	if got := svc.HeightBehindQuorum(3); got != 80 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 80 from the verified commit target", got)
	}
	// A lower target never lowers the watermark.
	svc.CatchUpTo(120)
	if got := svc.HeightBehindQuorum(3); got != 80 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 80 after a lower target", got)
	}
}

// The CommitQC hash promoted through a later body observation also proves
// the gap.
func TestHeightBehindQuorumVerifiedHashPromotion(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	svc.catchUpInProgress.Store(true)
	h := syncTSmallBlock(140).Hash()
	svc.committedTargets = map[comtypes.Hash]struct{}{}
	svc.committedTargets[h] = struct{}{}
	svc.observeCatchUpBlock(h, 140)
	if got := svc.HeightBehindQuorum(3); got != 40 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 40", got)
	}
	// An observation that no CommitQC named proves nothing.
	svc.observeCatchUpBlock(syncTSmallBlock(900).Hash(), 900)
	if got := svc.HeightBehindQuorum(3); got != 40 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 40 after an unauthenticated observation", got)
	}
}

// With a static validator mesh only the trusted peers vote.
func TestHeightBehindQuorumCountsOnlyTrustedPeers(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	validators := gateTestPeers(t, fp, 100, 100, 101)
	gateTestPeers(t, fp, 5_000, 5_000, 5_000, 5_000) // untrusted sybils
	fp.peerStatus.SetTrusted(validators)
	if got := svc.HeightBehindQuorum(3); got != 0 {
		t.Fatalf("HeightBehindQuorum(3) = %d, want 0: untrusted peers must not vote when a mesh is set", got)
	}
}

func TestHeightBehindQuorumLegacySwitch(t *testing.T) {
	defer synclegacy.HeightGate.SetForTest(true)()
	svc, fp, _ := newCatchUpTestService(t, 100)
	gateTestPeers(t, fp, 1_000_100, 100)
	if got := svc.HeightBehindQuorum(3); got != 1_000_000 {
		t.Fatalf("legacy HeightBehindQuorum = %d, want the old highest-peer gap 1000000", got)
	}
}
