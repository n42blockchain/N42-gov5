package transaction

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

// The hint feed and the import must pick the same signer type for the qs
// chain, or the sender cache (keyed by hash + signer) never hits.
func TestQSChainSignerMatchesLatest(t *testing.T) {
	cfg := params.ChainConfigByChainName("mainnet_qmdb_staggered")
	if cfg == nil {
		t.Skip("qs chain config not available")
	}
	imp := MakeSignerWithTimestamp(cfg, big.NewInt(13_700_000), 1_800_000_000)
	latest := LatestSignerForChainID(cfg.ChainID)
	t.Logf("import signer %T, latest %T, equal=%v", imp, latest, imp.Equal(latest))
	if !imp.Equal(latest) {
		t.Fatalf("import signer %T != latest %T: a feed recovering under LatestSignerForChainID is invisible to the import", imp, latest)
	}
}
