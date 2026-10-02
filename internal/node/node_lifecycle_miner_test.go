package node

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestNewNodeMinerEnabledFakerConsensus exercises the Miner==true startup
// path (authorizeMiningEngine, miner.Start/SetCoinbase) against the Faker
// consensus engine used by the eth-profile private chain, which needs no
// local signing key because its consensusSignerPlan mode is "none".
func TestNewNodeMinerEnabledFakerConsensus(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	cfg.NodeCfg.Miner = true
	cfg.Miner.Etherbase = "0x00000000000000000000000000000000001234"

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if n.Miner() == nil {
		t.Fatal("expected a non-nil miner after Start with Miner enabled")
	}

	eb, err := n.Etherbase()
	if err != nil {
		t.Fatalf("Etherbase: %v", err)
	}
	if eb.Hex() == (types.Address{}).Hex() {
		t.Fatal("expected a non-zero configured etherbase")
	}
}

// TestNewNodeAccessors exercises the small pure accessor methods not
// otherwise reached by lifecycle tests.
func TestNewNodeAccessors(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if n.Engine() == nil {
		t.Fatal("expected a non-nil consensus engine")
	}
	if n.ChainDb() == nil {
		t.Fatal("expected a non-nil chain database")
	}
	if n.Miner() == nil {
		t.Fatal("expected a non-nil miner")
	}
}

func TestSplitTagsFlag(t *testing.T) {
	cases := map[string]map[string]string{
		"":              {},
		"a=1":           {"a": "1"},
		"a=1,b=2":       {"a": "1", "b": "2"},
		"a=1,,b=2":      {"a": "1", "b": "2"},
		"novaluehere":   {},
		"a=1,bad,b=2=2": {"a": "1"},
	}
	for in, want := range cases {
		got := SplitTagsFlag(in)
		if len(got) != len(want) {
			t.Fatalf("SplitTagsFlag(%q) = %v, want %v", in, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("SplitTagsFlag(%q)[%q] = %q, want %q", in, k, got[k], v)
			}
		}
	}
}
