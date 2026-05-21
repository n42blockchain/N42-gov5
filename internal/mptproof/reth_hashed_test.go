package mptproof

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const productionRethDB2k = `D:\reth2k\db`

// TestFullProofBytes_Production_USDC_RethHashed validates that
// using reth's HashedAccounts / HashedStorages tables directly (no
// secondary index we built) produces correct EIP-1186 proofs.
//
// USDC account proof:
//
//	expected wall time: tens of milliseconds
//	  (HashedAccounts is sorted by keccak(addr), 7-nibble prefix
//	   matches ~6 leaves)
//
// USDC storage proof:
//
//	expected wall time: seconds to tens of seconds at hops within
//	the address-keccak portion (heavy-account prefix overlap with
//	other heavy accounts is structural; addressed only by Phase A.5
//	dense commitment domain).
func TestFullProofBytes_Production_USDC_RethHashed(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionChaindataDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionChaindataDir)
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	t0 := time.Now()
	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	g, err := New(Config{
		ChaindataDir: productionChaindataDir,
		Leaves:       src,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close()
	t.Logf("setup: %s", time.Since(t0).Truncate(time.Millisecond))

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	// ----- Account proof -----
	tWalk := time.Now()
	proof, err := g.LatestAccountProof(usdc)
	if err != nil {
		t.Fatalf("LatestAccountProof: %v", err)
	}
	t.Logf("acct walk: %s — hops=%d siblings=%d leafFound=%v",
		time.Since(tWalk).Truncate(time.Millisecond),
		len(proof.Walk.Hops), len(proof.Walk.CollectSiblings()), proof.LeafFound)

	tAcct := time.Now()
	pb, err := g.FullAccountProofBytes(proof)
	if err != nil {
		t.Fatalf("FullAccountProofBytes: %v", err)
	}
	dAcct := time.Since(tAcct)
	totalBytes := 0
	for _, n := range pb {
		totalBytes += len(n)
	}
	t.Logf("acct FullProofBytes: %s — %d nodes / %d bytes",
		dAcct.Truncate(time.Millisecond), len(pb), totalBytes)

	got, found, verr := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
	if verr != nil {
		t.Fatalf("acct oracle: %v", verr)
	}
	if !found {
		t.Fatal("acct oracle: !found")
	}
	if !bytes.Equal(got, proof.LeafValue) {
		t.Errorf("acct oracle value mismatch")
	}

	// ----- Storage proof: slots 0 and 1 -----
	var slots [2][32]byte
	slots[0][31] = 0
	slots[1][31] = 1

	storProofs, err := g.LatestStorageProofs(usdc, slots[:])
	if err != nil {
		t.Fatalf("LatestStorageProofs: %v", err)
	}
	if len(storProofs) != 2 {
		t.Fatalf("expected 2 storage proofs, got %d", len(storProofs))
	}

	for i, sp := range storProofs {
		tStor := time.Now()
		spb, ferr := g.FullStorageProofBytes(sp)
		if ferr != nil {
			t.Fatalf("FullStorageProofBytes slot %d: %v", i, ferr)
		}
		dStor := time.Since(tStor)
		sTotal := 0
		for _, n := range spb {
			sTotal += len(n)
		}
		t.Logf("stor slot %d: hops=%d, FullProofBytes %s — %d nodes / %d bytes",
			i, len(sp.Walk.Hops),
			dStor.Truncate(time.Millisecond), len(spb), sTotal)
	}

	// Operational summary
	dump := strings.Join([]string{
		fmt.Sprintf("source       = %s (HashedAccounts + HashedStorages)", productionRethDB2k),
		fmt.Sprintf("acct proof   = %d nodes / %d bytes / %s wall", len(pb), totalBytes, dAcct.Truncate(time.Millisecond)),
		fmt.Sprintf("oracle       = PASS for %d-byte account value", len(got)),
	}, "\n  ")
	t.Logf("\n  %s", dump)
}
