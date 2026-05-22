package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestProbe_USDCDepth6Variants: explores whether reth stores anything
// at depth 6 under USDC [0,0,0,0,a,0]. Also checks if reth uses a
// DIFFERENT key encoding (packed nibbles) for deeper rows.
func TestProbe_USDCDepth6Variants(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, _ := NewRethHashedLeafSource(productionRethDB2k, 4096)
	defer src.Close()

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])

	tx, _ := src.db.BeginRo(context.Background())
	defer tx.Rollback()
	c, _ := tx.CursorDupSort(rethStoragesTrieTable)
	defer c.Close()

	// Walk all USDC storage trie rows whose key starts with
	// [0,0,0,0,a] (nibble path). Dump first 20 to see depths.
	t.Logf("=== USDC StoragesTrie rows starting with [0,0,0,0,a] ===")
	// Seek to USDC + an empty subkey.
	v, _ := c.SeekBothRange(usdcHash[:], nil)
	i := 0
	depth5RowSeen := false
	for ; v != nil && i < 30; _, v, _ = c.NextDup() {
		// First 65 bytes of dup-value = StoredNibblesSubKey
		if len(v) < 65 {
			continue
		}
		subkey := v[:65]
		// length byte at position 64
		nibLen := int(subkey[64])
		// nibble payload at bytes [0..(nibLen)]
		if nibLen > 64 {
			nibLen = 64
		}
		nibPath := subkey[:nibLen]
		// only print rows whose prefix is [0,0,0,0,a]
		if nibLen >= 5 && nibPath[0] == 0 && nibPath[1] == 0 && nibPath[2] == 0 && nibPath[3] == 0 && nibPath[4] == 0xa {
			i++
			t.Logf("  depth=%d nibs=%v val_len=%d", nibLen, nibPath, len(v))
			if nibLen == 5 {
				depth5RowSeen = true
			}
		}
		if nibLen > 0 && nibPath[0] > 0 {
			// past [0,0,0,0,a] range; sorted scan complete
			break
		}
	}
	_ = depth5RowSeen

	// How many USDC HashedStorages slots share prefix [0,0,0,0,a,0]?
	hsc, _ := tx.CursorDupSort(rethHashedStoragesTable)
	defer hsc.Close()
	v2, _ := hsc.SeekBothRange(usdcHash[:], []byte{0x00, 0x00, 0xa0})
	count := 0
	t.Logf("=== USDC HashedStorages slots with prefix 0x0000a0 (=nibbles [0,0,0,0,a,0]) ===")
	for ; v2 != nil; _, v2, _ = hsc.NextDup() {
		if len(v2) < 32 {
			continue
		}
		slotHash := v2[:32]
		// First three bytes: 0x00 0x00 0xa0?
		if slotHash[0] != 0 || slotHash[1] != 0 {
			break
		}
		if slotHash[2] != 0xa0 {
			if slotHash[2] > 0xa0 {
				break
			}
			continue
		}
		count++
		if count <= 5 {
			t.Logf("  slot %d = %x (val[:8]=%x)", count, slotHash[:8], v2[32:min(40, len(v2))])
		}
	}
	t.Logf("total slots with prefix 0000a0: %d", count)
}

