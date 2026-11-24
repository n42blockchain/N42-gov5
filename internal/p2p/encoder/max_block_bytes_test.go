package encoder

import (
	"bytes"
	"testing"
)

// restoreCaps undoes SetMaxBlockBytes so package-level caps do not leak
// between tests.
func restoreCaps(t *testing.T) {
	gossip, chunk := MaxGossipSize, MaxBlockChunkSize
	t.Cleanup(func() { MaxGossipSize, MaxBlockChunkSize = gossip, chunk })
}

// Profile 1 (B15/AM2): maxBlockBytes is the only block bound, so the 1 MiB
// gossip default is raised to it and every derived cap follows.
func TestSetMaxBlockBytesIsTheOnlyBlockBound(t *testing.T) {
	restoreCaps(t)
	const n = uint64(64 << 20)
	SetMaxBlockBytes(n)
	if MaxGossipSize != n || MaxBlockChunkSize != n {
		t.Fatalf("caps gossip=%d chunk=%d, want both %d", MaxGossipSize, MaxBlockChunkSize, n)
	}
	if got := MaxWireMessageSize(); got != n {
		t.Fatalf("MaxWireMessageSize = %d, want %d", got, n)
	}
	if got := MaxGossipWireSize(); got < n {
		t.Fatalf("router cap %d below the block bound %d", got, n)
	}

	// A gossip payload of several MiB (above the legacy 1 MiB default)
	// round-trips; one byte above the bound does not.
	big := &rawTestBytes{data: bytes.Repeat([]byte{0xab}, 3<<20)}
	var buf bytes.Buffer
	if _, err := (SszNetworkEncoder{}).EncodeGossip(&buf, big); err != nil {
		t.Fatalf("3 MiB gossip refused under a 64 MiB bound: %v", err)
	}
	out := &rawTestBytes{}
	if err := (SszNetworkEncoder{}).DecodeGossip(buf.Bytes(), out); err != nil || len(out.data) != len(big.data) {
		t.Fatalf("3 MiB gossip decode: err=%v len=%d", err, len(out.data))
	}
	over := &rawTestBytes{data: make([]byte, n+1)}
	if _, err := (SszNetworkEncoder{}).EncodeGossip(new(bytes.Buffer), over); err == nil {
		t.Fatal("gossip above maxBlockBytes accepted")
	}
	if _, err := EncodeWithMaxLengthLimit(new(bytes.Buffer), over, MaxBlockChunkSize); err == nil {
		t.Fatal("block chunk above maxBlockBytes accepted")
	}
}

// Zero leaves the caps of a chain outside the profile untouched.
func TestSetMaxBlockBytesZeroIsNoop(t *testing.T) {
	restoreCaps(t)
	gossip, chunk := MaxGossipSize, MaxBlockChunkSize
	SetMaxBlockBytes(0)
	if MaxGossipSize != gossip || MaxBlockChunkSize != chunk {
		t.Fatal("SetMaxBlockBytes(0) changed the caps")
	}
}

// Without the setter (non-profile chains) the legacy defaults hold: gossip
// 1 MiB unless the env raises it, block chunks at least 64 MiB.
func TestLegacyCapsUnchangedWithoutSetter(t *testing.T) {
	if MaxGossipSize != sizeFromEnvMB("N42_MAX_GOSSIP_MB", 1<<20) {
		t.Fatalf("gossip cap %d is not the legacy env/default value", MaxGossipSize)
	}
	if MaxBlockChunkSize < StandardBlockBytes {
		t.Fatalf("block chunk cap %d below 64 MiB", MaxBlockChunkSize)
	}
}
