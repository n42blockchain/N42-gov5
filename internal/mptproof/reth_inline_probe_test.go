package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestProbe_USDCSlotsNearPrefix walks USDC's HashedStorages dups
// and prints the first ~20 slot hashes to verify the format
// and the actual nibble distribution.
func TestProbe_USDCSlotsNearPrefix(t *testing.T) {
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

	c, _ := tx.CursorDupSort(rethHashedStoragesTable)
	defer c.Close()

	// Seek to USDC and dump first 10 slot hashes.
	v, err := c.SeekBothRange(usdcHash[:], nil)
	if err != nil {
		t.Fatalf("SeekBothRange: %v", err)
	}
	t.Logf("USDC addrHash = %x", usdcHash[:])
	t.Logf("=== first 10 USDC slot hashes ===")
	i := 0
	for ; v != nil && i < 10; _, v, err = c.NextDup() {
		if err != nil {
			t.Fatalf("NextDup: %v", err)
		}
		slotHash := v[:32]
		t.Logf("  slot[%d] = %x (first 6 nibbles: %d %d %d %d %d %d)",
			i, slotHash[:8],
			slotHash[0]>>4, slotHash[0]&0xf,
			slotHash[1]>>4, slotHash[1]&0xf,
			slotHash[2]>>4, slotHash[2]&0xf)
		i++
	}

	// Seek specifically to slot 0 = keccak prefix 0x290dec
	t.Logf("=== USDC slots starting with 0x290d (slot 0 territory) ===")
	v, err = c.SeekBothRange(usdcHash[:], []byte{0x29, 0x0d})
	if err != nil {
		t.Fatalf("SeekBothRange [0x29 0x0d]: %v", err)
	}
	i = 0
	for ; v != nil && i < 20; _, v, err = c.NextDup() {
		if err != nil {
			t.Fatalf("NextDup: %v", err)
		}
		slotHash := v[:32]
		if !(slotHash[0] == 0x29 && slotHash[1] == 0x0d) {
			t.Logf("  (out of 0x290d range at slot %x..., breaking)", slotHash[:4])
			break
		}
		valDump := v[32:]
		if len(valDump) > 8 {
			valDump = valDump[:8]
		}
		t.Logf("  slot[%d] = %x... val=%x", i, slotHash[:8], valDump)
		i++
	}

	// Look for sibling at nibble 2 → prefix 0x29 0x0d 0xe2 (slot 0 nibble-c sibling at depth 5)
	t.Logf("=== seek 0x29 0x0d 0xe2 (looking for sibling at depth 5 nibble 2) ===")
	v, err = c.SeekBothRange(usdcHash[:], []byte{0x29, 0x0d, 0xe2})
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	if v != nil {
		t.Logf("first match >= 0x29 0x0d 0xe2: slot=%x", v[:8])
	}
}

