package blspool

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestRustCrossClientCommitteeEvidenceFixture(t *testing.T) {
	var seed [32]byte
	for i := range seed {
		seed[i] = byte(i)
	}
	pool, err := NewSimulatedPool(PoolConfig{
		Seed: seed, PoolSize: 64, CommitteeSize: 8, RampBlocks: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	ce, err := pool.BuildSimulatedCE(42, types.Hash{0x11}, types.Hash{0x22})
	if err != nil {
		t.Fatal(err)
	}
	want := types.HexToHash("0x944226263f2d69c82fa4ac757209043cea0665a36e1d9e1d5c493fe3cf392b31")
	if got := ce.BeaconRoot(); got != want {
		t.Fatalf("committee evidence root = %s, want Rust fixture %s", got, want)
	}
}
