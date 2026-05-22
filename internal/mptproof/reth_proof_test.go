package mptproof

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"
)

// TestRB3_USDCStorageSlot0_FullProof: the headline "完成世界状态"
// validation. Reads reth at D:\reth2k\db, walks USDC's slot 0
// (totalSupply), assembles the complete EIP-1186 proof byte
// stream, and verifies the chain (keccak(proof[i+1]) embeds in
// proof[i]).
//
// Times every step. Target: sub-second.
func TestRB3_USDCStorageSlot0_FullProof(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, _ := NewRethHashedLeafSource(productionRethDB2k, 4096)
	defer src.Close()
	r := NewRethTrieReader(src)
	reader := NewRethBackedReader(src)

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])

	var slot [32]byte
	h2 := sha3.NewLegacyKeccak256()
	h2.Write(slot[:])
	var slotHash [32]byte
	h2.Sum(slotHash[:0])

	// --- get slot value via PlainStorageState ---
	composite := append(append([]byte{}, usdc...), slot[:]...)
	t0 := time.Now()
	upd, err := reader.Storage(composite)
	dRead := time.Since(t0)
	if err != nil || upd == nil {
		t.Fatalf("read slot 0 from reth: err=%v upd=%v", err, upd)
	}
	slotValue := make([]byte, upd.StorageLen)
	copy(slotValue, upd.Storage[:upd.StorageLen])
	t.Logf("slot read: %s — value=0x%x (%d B)", dRead, slotValue, len(slotValue))

	// --- walk ---
	t1 := time.Now()
	walk, err := WalkRethStorage(r, usdcHash[:], slotHash[:])
	dWalk := time.Since(t1)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}
	t.Logf("walk: %s — %d hops outcome=%v leafDepth=%d",
		dWalk, len(walk.Hops), walk.Outcome, walk.LeafDepth)

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

	total := dRead + dWalk + dBuild + dVerify
	t.Logf("HA-3c/RB-3 SUCCESS: USDC slot 0 end-to-end in %s (read=%s walk=%s build=%s verify=%s)",
		total.Truncate(time.Microsecond), dRead, dWalk, dBuild, dVerify)
	if total > time.Second {
		t.Errorf("total wall %s > 1s", total)
	}
}

