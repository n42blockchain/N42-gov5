package hotstuff

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestTimeoutPublishPeerTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		quorum int
		want   int
	}{
		{name: "single validator", quorum: 1, want: 0},
		{name: "four validators", quorum: 3, want: 2},
		{name: "seven validators", quorum: 5, want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeoutPublishPeerTarget(tt.quorum); got != tt.want {
				t.Fatalf("timeoutPublishPeerTarget(%d) = %d, want %d", tt.quorum, got, tt.want)
			}
		})
	}
}

func TestViewChangeDirectTargetsUsesConnectedPeersWithoutRegistryWarmup(t *testing.T) {
	t.Parallel()

	peer0 := peer.ID("validator-0")
	peer1 := peer.ID("validator-1")
	peer3 := peer.ID("validator-3")
	targets := viewChangeDirectTargets([]peer.ID{peer0, peer1, "", peer0, peer3})
	if len(targets) != 3 || targets[0] != peer0 || targets[1] != peer1 || targets[2] != peer3 {
		t.Fatalf("direct targets = %v, want [%s %s %s]", targets, peer0, peer1, peer3)
	}
}

func TestAuthenticatedRecoveryViewUsesCertificateButNotTimeoutWatermark(t *testing.T) {
	t.Parallel()

	state := &ConsensusState{
		View:                1001,
		ConsecutiveTimeouts: 9,
		LockedQC:            QuorumCertificate{View: 1002},
		LastCommittedQC:     QuorumCertificate{View: 999},
	}
	if got := authenticatedRecoveryView(state); got != 1003 {
		t.Fatalf("recovery view = %d, want 1003", got)
	}
	state.LockedQC.View = 1000
	if got := authenticatedRecoveryView(state); got != 1001 {
		t.Fatalf("stale certificate changed recovery view to %d", got)
	}
}
