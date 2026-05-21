package mptproof

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFullProofBytes_Production_USDC_HASHED is the hot-path counterpart
// of TestFullProofBytes_Production_USDC_SLOW. Same USDC proof, same
// oracle verify — but the LeafSource is HashedLeafSource backed by
// the bootstrap-built D:\n42-chaindata index, so the inline-sibling
// ScanAccounts cost (4×~3-4 min in the SLOW version) becomes 4×~5 ms
// cursor seeks.
//
// Expected speedup ~10,000x. Pass with `-timeout 2m`; if it ever
// approaches the 30 min budget of the SLOW test, the hashed index is
// not being hit and we've regressed.
func TestFullProofBytes_Production_USDC_HASHED(t *testing.T) {
	if testing.Short() {
		t.Skip("--short: skipping production-data test")
	}
	if _, err := os.Stat(filepath.Join(productionChaindataDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionChaindataDir)
	}
	// We still need the reth source as the base LeafSource for the
	// HashedLeafSource (account/storage point lookups + storage value
	// re-fetch under Option A).
	rethDir := `D:\reth2k\db`
	if _, err := os.Stat(filepath.Join(rethDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", rethDir)
	}

	tBase := time.Now()
	base, err := NewRethLeafSource(rethDir, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatalf("NewRethLeafSource: %v", err)
	}
	defer base.Close()

	// Open the Generator FIRST so the chaindata env is owned by it
	// (with HashedAccount + HashedStorageRef declared in its TableCfg
	// thanks to mpttrie.OpenUnifiedDB). Then attach HashedLeafSource
	// onto the same env via NewHashedLeafSourceFromDB to avoid
	// MDBX_BUSY from opening the env twice in the same process.
	g, err := New(Config{
		ChaindataDir: productionChaindataDir,
		Leaves:       base, // placeholder; will swap to hashed below
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close()

	hashed, err := NewHashedLeafSourceFromDB(g.UnifiedEnv(), base)
	if err != nil {
		t.Fatalf("NewHashedLeafSourceFromDB: %v", err)
	}
	g.SetLeafSource(hashed)
	t.Logf("setup: %s", time.Since(tBase).Truncate(time.Millisecond))

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	tWalk := time.Now()
	proof, err := g.LatestAccountProof(usdc)
	if err != nil {
		t.Fatalf("LatestAccountProof: %v", err)
	}
	t.Logf("walk: %s — hops=%d siblings=%d leafFound=%v leafLen=%d",
		time.Since(tWalk).Truncate(time.Millisecond),
		len(proof.Walk.Hops),
		len(proof.Walk.CollectSiblings()),
		proof.LeafFound, len(proof.LeafValue))

	tFull := time.Now()
	pb, err := g.FullAccountProofBytes(proof)
	if err != nil {
		t.Fatalf("FullAccountProofBytes: %v", err)
	}
	dFull := time.Since(tFull)
	totalBytes := 0
	for _, n := range pb {
		totalBytes += len(n)
	}
	t.Logf("FullAccountProofBytes: %s — %d nodes / %d bytes total",
		dFull.Truncate(time.Millisecond), len(pb), totalBytes)

	tOracle := time.Now()
	gotVal, found, verr := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
	if verr != nil {
		t.Fatalf("oracle verify: %v", verr)
	}
	if !found {
		t.Fatal("oracle: !found for USDC")
	}
	if !bytes.Equal(gotVal, proof.LeafValue) {
		t.Errorf("oracle value mismatch")
	}
	t.Logf("oracle verify: %s — value=%d bytes",
		time.Since(tOracle).Truncate(time.Millisecond), len(gotVal))

	// Regression guard: if the hashed index is hit, USDC should
	// complete in well under 30 s. SLOW path is 12+ min — anything
	// over 1 min here means we accidentally took the slow path.
	if dFull > 60*time.Second {
		t.Errorf("FullAccountProofBytes took %s — hashed index appears not engaged",
			dFull.Truncate(time.Second))
	}
}
