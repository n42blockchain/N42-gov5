package mptproof

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// TestRB3_USDCCleanSlot_FullProof: assembles a complete EIP-1186
// proof for a USDC storage slot whose walk landed on a HASH-ref
// leaf (so no inline-sibling reconstruction is needed for the
// terminal leaf itself). The slot is chosen by scanning the table
// rather than hardcoded — D:\reth2k has a documented
// StoragesTrie/HashedStorages sync mismatch at depth 5 for some
// nibbles, so a hardcoded slot 0 doesn't work end-to-end. A
// "clean" candidate sidesteps the env inconsistency.
//
// Target: sub-second wall.
func TestRB3_USDCCleanSlot_FullProof(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, _ := NewRethHashedLeafSource(productionRethDB2k, 4096)
	defer src.Close()
	r := NewRethTrieReader(src)

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])

	// Scan USDC's HashedStorages cursor for a "clean" slot: one
	// whose walk lands on a leaf via a HASH ref (not inline) at
	// the deepest hop. Such a slot needs no inline-sibling
	// reconstruction for the leaf-emit step.
	tx, _ := src.db.BeginRo(context.Background())
	defer tx.Rollback()
	c, _ := tx.CursorDupSort(rethHashedStoragesTable)
	defer c.Close()

	var slotHash [32]byte
	var slotValue []byte
	t0 := time.Now()
	v, _ := c.SeekBothRange(usdcHash[:], nil)
	for ; v != nil; _, v, _ = c.NextDup() {
		if len(v) < 32 {
			continue
		}
		var sh [32]byte
		copy(sh[:], v[:32])
		walk, err := WalkRethStorage(r, usdcHash[:], sh[:])
		if err != nil || len(walk.Hops) == 0 {
			continue
		}
		deepest := walk.Hops[len(walk.Hops)-1]
		hashBit := deepest.Branch.HasHash & (uint16(1) << uint16(deepest.TargetNibble))
		if hashBit == 0 || walk.Outcome != mpttrie.LandedOnLeaf {
			continue
		}
		// Also require all of the deepest hop's siblings to be
		// hashed (no inline siblings). This gives a fully-decidable
		// proof byte stream end-to-end.
		if deepest.Branch.HasState != deepest.Branch.HasHash {
			continue
		}
		slotHash = sh
		slotValue = append([]byte{}, v[32:]...)
		break
	}
	dScan := time.Since(t0)
	if slotValue == nil {
		t.Skipf("no clean USDC slot found in first scan (env may need refresh)")
	}
	t.Logf("clean slot found: %s — slot_hash=%x value=%x (%d B)",
		dScan, slotHash[:8], slotValue, len(slotValue))

	// --- walk (re-do for timing on chosen slot) ---
	t1 := time.Now()
	walk, err := WalkRethStorage(r, usdcHash[:], slotHash[:])
	dWalk := time.Since(t1)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}
	t.Logf("walk: %s — %d hops outcome=%v leafDepth=%d",
		dWalk, len(walk.Hops), walk.Outcome, walk.LeafDepth)
	for i, hop := range walk.Hops {
		t.Logf("  hop %d: depth=%d nib=%x state=0x%04x tree=0x%04x hash=0x%04x",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, hop.Branch.HasTree, hop.Branch.HasHash)
	}

	// --- assemble proof ---
	t2 := time.Now()
	proof, storageRoot, err := BuildRethStorageProof(r, usdcHash[:], slotHash[:], slotValue, walk)
	dBuild := time.Since(t2)
	if err != nil {
		t.Fatalf("BuildRethStorageProof: %v", err)
	}
	t.Logf("build: %s — %d proof nodes, storageRoot=%x",
		dBuild, len(proof), storageRoot[:8])

	totalBytes := 0
	sizes := []string{}
	for i, n := range proof {
		totalBytes += len(n)
		sizes = append(sizes, fmt.Sprintf("%d:%dB", i, len(n)))
	}
	t.Logf("proof shape: %d nodes, %d total bytes, sizes=[%s]",
		len(proof), totalBytes, strings.Join(sizes, " "))

	// --- verify chain ---
	t3 := time.Now()
	mismatch := VerifyProofChain(proof)
	dVerify := time.Since(t3)
	if mismatch != -1 {
		// Compute keccak(proof[mismatch+1]) and dump.
		hsum := sha3.NewLegacyKeccak256()
		hsum.Write(proof[mismatch+1])
		var leafHash [32]byte
		hsum.Sum(leafHash[:0])
		t.Logf("keccak(proof[%d]) = %x", mismatch+1, leafHash[:])
		t.Logf("expected to be embedded in proof[%d] as 0xa0||hash", mismatch)
		// Dump all hashes in proof[mismatch] (every 33 bytes starting from the third byte = list header)
		parent := proof[mismatch]
		t.Logf("FULL proof[%d] = %x", mismatch, parent)
		// Search for our leaf hash directly in parent.
		// Just scan all 33-byte 0xa0 references.
		t.Logf("scanning %d bytes for 0xa0||leafHash at any offset", len(parent))
		// dump for debugging
		for i, n := range proof {
			marker := ""
			if i == mismatch || i == mismatch+1 {
				marker = "  <-- mismatch"
			}
			dumpLen := 40
			if len(n) < dumpLen {
				dumpLen = len(n)
			}
			t.Logf("  proof[%d] = %x%s", i, n[:dumpLen], marker)
		}
		t.Fatalf("chain mismatch at index %d", mismatch)
	}
	t.Logf("verify: %s — chain OK", dVerify)

	total := dScan + dWalk + dBuild + dVerify
	t.Logf("RB-3 SUCCESS: USDC clean-slot end-to-end in %s (scan=%s walk=%s build=%s verify=%s)",
		total.Truncate(time.Microsecond), dScan, dWalk, dBuild, dVerify)
	if total > time.Second {
		t.Errorf("total wall %s > 1s", total)
	}
}

