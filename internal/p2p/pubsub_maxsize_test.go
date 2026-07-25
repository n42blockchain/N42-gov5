package p2p

import (
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

func withEncoderGossipLimit(t *testing.T, bytes uint64) {
	t.Helper()
	oldGossip, oldChunk := encoder.MaxGossipSize, encoder.MaxChunkSize
	encoder.MaxGossipSize, encoder.MaxChunkSize = bytes, bytes
	t.Cleanup(func() {
		encoder.MaxGossipSize, encoder.MaxChunkSize = oldGossip, oldChunk
	})
}

// TestGossipMaxSizeTracksEncoderBound is the P2 regression: the router cap used
// to be a hard-coded 1 MiB const, so raising N42_MAX_GOSSIP_MB moved the
// producer's packing budget and the encoder's bound while libp2p kept refusing
// anything over 1 MiB — the chain would lose gossip propagation entirely and
// limp on direct pushes.
func TestGossipMaxSizeTracksEncoderBound(t *testing.T) {
	withEncoderGossipLimit(t, 1<<20)
	small := GossipMaxSize()

	withEncoderGossipLimit(t, 16<<20)
	large := GossipMaxSize()

	if large <= small {
		t.Fatalf("router cap did not follow the encoder bound: 1 MiB -> %d, 16 MiB -> %d", small, large)
	}
	if large < 16<<20 {
		t.Fatalf("router cap %d is below the 16 MiB payload the encoder now accepts", large)
	}
}

// TestGossipMaxSizeCoversSnappyExpansion pins the headroom: EncodeGossip bounds
// the UNCOMPRESSED payload and then snappy-encodes it, and snappy expands
// incompressible input. A router cap set exactly at MaxGossipSize would drop
// messages the encoder considers valid.
func TestGossipMaxSizeCoversSnappyExpansion(t *testing.T) {
	withEncoderGossipLimit(t, 1<<20)
	if GossipMaxSize() <= encoder.MaxGossipSize {
		t.Fatalf("router cap %d leaves no room for snappy expansion above the %d byte payload bound",
			GossipMaxSize(), encoder.MaxGossipSize)
	}
}
