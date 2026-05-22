package mptproof

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"
)

// rethStorageTrieV2EnvPath: location of the transcoded v2 env.
// Override via N42_STORAGE_TRIE_V2 for non-default locations.
var rethStorageTrieV2EnvPath = func() string {
	if v := os.Getenv("N42_STORAGE_TRIE_V2"); v != "" {
		return v
	}
	return `D:\n42-storage-trie-v2`
}()

// TestRB5a_USDCStorageBranch_V2Matches_V1 reads the same USDC
// storage branch via both readers and asserts they decode to
// byte-identical BranchNode values.
//
// This is RB-5a's end-to-end acceptance: the 33-byte packed
// subkey format produces semantically equivalent reads to the
// 65-byte v1 form. After the transcode tool runs and populates
// D:\n42-storage-trie-v2, this test ratifies the compression.
//
// Skipped if the V2 env isn't present.
func TestRB5a_USDCStorageBranch_V2Matches_V1(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("reth env %s not present", productionRethDB2k)
	}
	if _, err := os.Stat(filepath.Join(rethStorageTrieV2EnvPath, "mdbx.dat")); err != nil {
		t.Skipf("v2 transcoded env %s not present — run cmd/n42-storage-trie-transcode first",
			rethStorageTrieV2EnvPath)
	}

	v1src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("open v1: %v", err)
	}
	defer v1src.Close()
	v1 := NewRethTrieReader(v1src)

	v2, err := OpenRethTrieReaderV2(rethStorageTrieV2EnvPath, 64)
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}
	defer v2.Close()

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])

	// Probe several nibble paths to verify v1/v2 agreement at each.
	probePaths := [][]byte{
		nil,
		{0},
		{1},
		{0xa},
		{0xb},
		{0, 0, 0, 0, 2},
		{0, 0, 0, 0, 0xa},
	}
	for _, path := range probePaths {
		b1, ok1, e1 := v1.StorageBranchAt(usdcHash[:], path)
		b2, ok2, e2 := v2.StorageBranchAt(usdcHash[:], path)
		if e1 != nil || e2 != nil {
			t.Fatalf("path %x: v1 err=%v v2 err=%v", path, e1, e2)
		}
		if ok1 != ok2 {
			t.Errorf("path %x: ok mismatch v1=%v v2=%v", path, ok1, ok2)
			continue
		}
		if !ok1 {
			t.Logf("path %x: absent in both (consistent)", path)
			continue
		}
		// Compare structural fields.
		if b1.HasState != b2.HasState ||
			b1.HasTree != b2.HasTree ||
			b1.HasHash != b2.HasHash {
			t.Errorf("path %x: mask mismatch\n  v1: state=%04x tree=%04x hash=%04x\n  v2: state=%04x tree=%04x hash=%04x",
				path, b1.HasState, b1.HasTree, b1.HasHash,
				b2.HasState, b2.HasTree, b2.HasHash)
			continue
		}
		if len(b1.Hashes) != len(b2.Hashes) {
			t.Errorf("path %x: hash-count mismatch v1=%d v2=%d",
				path, len(b1.Hashes), len(b2.Hashes))
			continue
		}
		for i := range b1.Hashes {
			if b1.Hashes[i] != b2.Hashes[i] {
				t.Errorf("path %x: hash[%d] mismatch\n  v1: %x\n  v2: %x",
					path, i, b1.Hashes[i][:8], b2.Hashes[i][:8])
				break
			}
		}
		t.Logf("path %x: v1 == v2 (state=%04x %d hashes)",
			path, b1.HasState, len(b1.Hashes))
	}
}
