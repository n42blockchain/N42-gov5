package params

import (
	"math/big"
	"testing"
)

// parityStaggered is mainnet_qmdb_staggered with EthereumParityTime set.
func parityStaggered(t *testing.T, parity uint64) *ChainConfig {
	t.Helper()
	cfg := *ChainConfigByChainName("mainnet_qmdb_staggered")
	cfg.EthereumParityTime = new(big.Int).SetUint64(parity)
	return &cfg
}

// EthereumParityTime switches EOF off again and leaves every other rule of
// the chain as it was; before it nothing changes.
func TestEthereumParityTimeEndsEOF(t *testing.T) {
	const fusaka, parity = uint64(1765238400), uint64(1800000000)
	cfg := parityStaggered(t, parity)
	plain := ChainConfigByChainName("mainnet_qmdb_staggered")

	before := activeRuleNames(cfg.RulesWithTimestamp(4_000_000, parity-1))
	if want := activeRuleNames(plain.RulesWithTimestamp(4_000_000, parity-1)); before != want {
		t.Fatalf("rules before the parity time changed:\n got  %s\n want %s", before, want)
	}
	if !cfg.IsEOF(fusaka) || !cfg.IsEOF(parity-1) {
		t.Fatal("EOF must stay active between eofTime and the parity time")
	}

	r := cfg.RulesWithTimestamp(4_000_000, parity)
	if r.IsEOF || cfg.IsEOF(parity) || cfg.IsEOF(parity+1_000_000) {
		t.Fatal("EOF must be off from the parity time")
	}
	if !r.IsEthereumParity || !r.IsFusaka || !r.IsOsaka || !r.IsPrague {
		t.Fatalf("parity must leave the fork rules on: %s", activeRuleNames(r))
	}
}

// A header commits to its requests under Prague rules and the parity time
// together, however Prague was reached.
func TestCommitsRequestsHash(t *testing.T) {
	const pectra, parity = uint64(1746612311), uint64(1800000000)
	cfg := parityStaggered(t, parity)
	if cfg.CommitsRequestsHash(4_000_000, parity-1) {
		t.Fatal("no commitment before the parity time")
	}
	if !cfg.CommitsRequestsHash(4_000_000, parity) {
		t.Fatal("a Pectra chain commits from the parity time")
	}
	if ChainConfigByChainName("mainnet_qmdb_staggered").CommitsRequestsHash(4_000_000, parity) {
		t.Fatal("no commitment without a parity time")
	}
	early := parityStaggered(t, 0)
	if early.CommitsRequestsHash(3_935_000, pectra-1) {
		t.Fatal("no commitment before Prague rules, even under the parity time")
	}
	if !early.CommitsRequestsHash(4_000_000, pectra) {
		t.Fatal("commitment from Pectra when the parity time is already past")
	}
}

// A parity time that has passed cannot be rescheduled.
func TestEthereumParityTimeCompat(t *testing.T) {
	stored := parityStaggered(t, 100)
	if err := stored.CheckCompatible(parityStaggered(t, 200), 150); err == nil {
		t.Fatal("moving a passed parity time must be refused")
	}
	if err := stored.CheckCompatible(parityStaggered(t, 200), 50); err != nil {
		t.Fatalf("moving a future parity time: %v", err)
	}
}
