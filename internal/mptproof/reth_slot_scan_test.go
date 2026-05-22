package mptproof

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// TestProbe_FindUSDCSlot_CleanProof scans 100 USDC storage slots
// and reports which ones have a CLEAN proof path (deepest hop's
// hash_mask bit for the target nibble is SET — no inline-sibling
// reconstruction required from PlainStorageState).
//
// A clean slot lets us validate the full proof pipeline end-to-end
// without depending on env consistency at the inline-sibling layer.
func TestProbe_FindUSDCSlot_CleanProof(t *testing.T) {
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

	// Pull the first 50 USDC slot-hashes from HashedStorages.
	tx, _ := src.db.BeginRo(context.Background())
	defer tx.Rollback()
	c, _ := tx.CursorDupSort(rethHashedStoragesTable)
	defer c.Close()
	v, _ := c.SeekBothRange(usdcHash[:], nil)

	type cand struct {
		slotHash [32]byte
		hops     int
		outcome  mpttrie.WalkOutcome
		deepHash bool
	}
	var clean []cand
	i := 0
	for ; v != nil && i < 50; _, v, _ = c.NextDup() {
		if len(v) < 32 {
			continue
		}
		var sh [32]byte
		copy(sh[:], v[:32])

		walk, err := WalkRethStorage(r, usdcHash[:], sh[:])
		if err != nil || len(walk.Hops) == 0 {
			i++
			continue
		}
		deepest := walk.Hops[len(walk.Hops)-1]
		hashBit := deepest.Branch.HasHash & (uint16(1) << uint16(deepest.TargetNibble))
		isClean := hashBit != 0 && walk.Outcome == mpttrie.LandedOnLeaf

		// Decode value to know what kind of slot we're looking at.
		val := v[32:]
		valDump := val
		if len(valDump) > 8 {
			valDump = valDump[:8]
		}
		t.Logf("  slot[%d] %x... hops=%d outcome=%v deepHashBit=%v val=%x",
			i, sh[:6], len(walk.Hops), walk.Outcome, hashBit != 0, valDump)
		if isClean {
			clean = append(clean, cand{slotHash: sh, hops: len(walk.Hops), outcome: walk.Outcome, deepHash: true})
		}
		i++
	}

	t.Logf("found %d clean USDC slots out of %d scanned", len(clean), i)
	if len(clean) > 0 {
		c0 := clean[0]
		t.Logf("first clean candidate: slot_hash=%x hops=%d", c0.slotHash[:], c0.hops)
	}
}

// TestProbe_MinimalUSDCSlotWalk also computes the well-known
// keccak for explicit slots {0,1,2,3,10,11} so we have a quick
// reference for which one to pick.
func TestProbe_MinimalUSDCSlotWalk(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	slots := []uint64{0, 1, 2, 3, 10, 11, 100, 1000}
	for _, s := range slots {
		var slot [32]byte
		binary.BigEndian.PutUint64(slot[24:], s)
		h := sha3.NewLegacyKeccak256()
		h.Write(slot[:])
		hash := h.Sum(nil)
		fmt.Printf("slot %d  keccak=%x (first6 nibbles %d,%d,%d,%d,%d,%d)\n",
			s, hash[:8],
			hash[0]>>4, hash[0]&0xf,
			hash[1]>>4, hash[1]&0xf,
			hash[2]>>4, hash[2]&0xf)
	}
}
