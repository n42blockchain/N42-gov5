package params

import "testing"

// mainnet_qmdb_staggered follows the Ethereum forks it activates: no EOF at
// any time (no scheduled Ethereum fork has it).
func TestMainnetQMDBStaggeredHasNoEOF(t *testing.T) {
	cfg := ChainConfigByChainName("mainnet_qmdb_staggered")
	if cfg.EOFTime != nil {
		t.Fatalf("eofTime = %v, want unset", cfg.EOFTime)
	}
	for _, ts := range []uint64{1765238400, 1784372000, 4_000_000_000} {
		if cfg.IsEOF(ts) || cfg.RulesWithTimestamp(9_000_000, ts).IsEOF {
			t.Fatalf("EOF active at %d", ts)
		}
	}
}
