package node

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// computeGenesisHash builds a chain's genesis exactly the way node startup does
// and returns its hash.
func computeGenesisHash(t *testing.T, chain string) string {
	t.Helper()
	g := internal.GenesisByChainName(chain)
	if g == nil {
		t.Fatalf("%s: no genesis definition", chain)
	}
	db := memdb.NewTestDB(t)
	var got string
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, err := WriteGenesisBlock(tx, g)
		if err != nil {
			return err
		}
		got = blk.Hash().Hex()
		return nil
	}); err != nil {
		t.Fatalf("%s: write genesis: %v", chain, err)
	}
	return got
}

// TestGenesisHashesMatchChainspecs asserts that every replay-built chain's
// registered genesis hash is what the chainspec actually produces.
//
// This is the check whose absence let three separate defects live undetected:
// the constant disagreed with the datadir AND with a fresh init, six chains
// shared one constant so their p2p fork digests collapsed into each other, and
// replay injected genesis state from code after the header was sealed so the
// root never covered it. Any of those would have failed here on the first run.
//
// The registered hash is not cosmetic — node.go feeds it to the p2p layer as
// the fork digest and the Status handshake compares only that, so two chains
// sharing a value accept each other's peers.
func TestGenesisHashesMatchChainspecs(t *testing.T) {
	for _, chain := range []string{
		"mainnet_v2",
		"mainnet_mpt",
		"mainnet_v2_staggered",
		"mainnet_qmdb",
		"mainnet_qmdb_staggered",
		"qs_epoch_test",
	} {
		t.Run(chain, func(t *testing.T) {
			registered := params.GenesisHashByChainName(chain)
			if registered == nil {
				t.Fatalf("%s has no registered genesis hash; node startup rejects it as an unknown chain", chain)
			}
			if got := computeGenesisHash(t, chain); got != registered.Hex() {
				t.Fatalf("genesis hash drifted\n  computed:   %s\n  registered: %s\n"+
					"Either the chainspec/alloc changed and the constant needs updating, or "+
					"genesis construction changed and every datadir built from it is now unreproducible.",
					got, registered.Hex())
			}
		})
	}
}

// TestGenesisHashesAreDistinct guards the property the shared constant destroyed:
// distinct networks must not collide in the first four bytes, because that is
// exactly the p2p fork digest.
func TestGenesisHashesAreDistinct(t *testing.T) {
	seen := map[string]string{} // digest -> chain
	for _, chain := range []string{
		"mainnet", "testnet",
		"mainnet_v2_staggered", "mainnet_qmdb", "mainnet_qmdb_staggered", "qs_epoch_test",
	} {
		h := params.GenesisHashByChainName(chain)
		if h == nil {
			t.Fatalf("%s has no registered genesis hash", chain)
		}
		digest := h.Hex()[:10] // 0x + 4 bytes
		if other, dup := seen[digest]; dup {
			t.Fatalf("%s and %s share fork digest %s — their nodes would join the same gossip "+
				"topics and their Status handshakes would accept each other", chain, other, digest)
		}
		seen[digest] = chain
	}
}

// TestLegacyChainsKeepDeployedGenesis documents the deliberate exception: the two
// networks that are actually deployed keep the hash their peers expect, even
// though the header struct has since evolved and a fresh build no longer
// reproduces it. Changing these is a flag day, not a bug fix.
func TestLegacyChainsKeepDeployedGenesis(t *testing.T) {
	for chain, want := range map[string]string{
		"mainnet": params.MainnetGenesisHash.Hex(),
		"testnet": params.TestnetGenesisHash.Hex(),
	} {
		got := params.GenesisHashByChainName(chain)
		if got == nil || got.Hex() != want {
			t.Fatalf("%s must keep its deployed genesis hash %s, got %v", chain, want, got)
		}
		if computed := computeGenesisHash(t, chain); computed == want {
			t.Logf("note: %s now recomputes to its deployed hash again (%s); the exception could be retired", chain, want)
		}
	}
}
