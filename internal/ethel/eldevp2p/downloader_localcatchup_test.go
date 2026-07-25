package eldevp2p

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestLocalCatchUp_FreezerDirectBlockHash validates the core invariant the clean
// (ethexec-style) localCatchUp relies on: the freezer-direct BLOCKHASH source —
// headerc's stored h.Hash() — returns each block's TRUE canonical hash, with NO
// field reconstruction, NO MDBX seeding, NO GetHashFn ParentHash walk. It checks
// headerc ReadHeader(n).Hash() == the geth-ancient canonical hash for the whole
// BLOCKHASH window an executing block 25587084..25587115 would reach
// [25587084-256 .. 25587114]. If these all match, the adapter's headerHashReader
// resolves BLOCKHASH correctly for local execution.
//
// Reads this machine's weekly freezers; skips when either is absent (never breaks
// CI on a box without the data).
func TestLocalCatchUp_FreezerDirectBlockHash(t *testing.T) {
	cdir := filepath.Join("d:/n42-eth1", "chain", "freezer")
	gdir := "d:/geth/geth/chaindata/ancient/chain"
	if _, err := os.Stat(filepath.Join(cdir, "headerc.cidx")); err != nil {
		t.Skipf("headerc absent (%s) — skipping", cdir)
	}
	if _, err := os.Stat(gdir); err != nil {
		t.Skipf("geth ancient absent (%s) — skipping", gdir)
	}
	hr, err := ethel.OpenHeaderCompact(cdir)
	if err != nil {
		t.Skipf("OpenHeaderCompact: %v", err)
	}
	defer hr.Close()
	f, err := freezer.NewReadOnly(gdir)
	if err != nil {
		t.Skipf("geth freezer: %v", err)
	}
	defer f.Close()

	gethHash := func(n uint64) string {
		d, e := f.Ancient(freezer.TableHeaders, n)
		if e != nil {
			t.Fatalf("geth header %d: %v", n, e)
		}
		h, e := ethel.DecodeGethHeader(d)
		if e != nil {
			t.Fatalf("decode geth header %d: %v", n, e)
		}
		return h.Hash().Hex()
	}

	// The union of all BLOCKHASH windows for executing blocks 25587084..25587115:
	// the lowest ancestor is (25587084-256) and the highest is 25587114.
	const lo, hi = 25587084 - 256, 25587114
	var checked, mismatch int
	for n := uint64(lo); n <= hi; n++ {
		h, herr := hr.ReadHeader(n)
		if herr != nil || h == nil {
			t.Fatalf("headerc ReadHeader %d: %v", n, herr)
		}
		checked++
		if got, want := h.Hash().Hex(), gethHash(n); got != want {
			mismatch++
			if mismatch <= 3 {
				t.Errorf("block %d: headerc h.Hash()=%s != geth canonical %s", n, got, want)
			}
		}
	}
	if mismatch != 0 {
		t.Fatalf("%d/%d headerc hashes diverged from geth — freezer-direct BLOCKHASH source is WRONG", mismatch, checked)
	}
	t.Logf("freezer-direct BLOCKHASH OK: %d headerc h.Hash() values all match geth canonical (no reconstruction needed)", checked)
}
