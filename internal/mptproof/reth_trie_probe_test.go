package mptproof

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRethTrieProbe_FirstKeys walks the AccountsTrie + StoragesTrie
// cursors and prints the first few rows verbatim, so we can see
// the actual stored-key format reth uses.
func TestRethTrieProbe_FirstKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	tx, _ := src.db.BeginRo(context.Background())
	defer tx.Rollback()

	// AccountsTrie cursor: first 5 rows.
	c, _ := tx.Cursor(rethAccountsTrieTable)
	t.Logf("=== AccountsTrie first 5 rows ===")
	i := 0
	for k, v, err := c.First(); k != nil && i < 5; k, v, err = c.Next() {
		if err != nil {
			t.Logf("Next err: %v", err)
			break
		}
		t.Logf("  row %d: key_len=%d key=%x val_len=%d val_first16=%x",
			i, len(k), k, len(v), v[:min(16, len(v))])
		i++
	}
	c.Close()

	// StoragesTrie cursor: first 5 rows (DupSort).
	c2, _ := tx.CursorDupSort(rethStoragesTrieTable)
	t.Logf("=== StoragesTrie first 5 dup rows (FULL value) ===")
	i = 0
	for k, v, err := c2.First(); k != nil && i < 5; k, v, err = c2.Next() {
		if err != nil {
			t.Logf("Next err: %v", err)
			break
		}
		t.Logf("  row %d: key=%x", i, k)
		t.Logf("    val_len=%d val_full=%x", len(v), v)
		i++
	}
	c2.Close()

	// AccountsTrie row 0 full value:
	c3, _ := tx.Cursor(rethAccountsTrieTable)
	k, v, _ := c3.First()
	t.Logf("=== AccountsTrie row 0 FULL value ===")
	t.Logf("key=%x val_len=%d", k, len(v))
	t.Logf("val[0..6]   = %x (3 LE u16 masks)", v[:6])
	t.Logf("val[6..38]  = %x (32B optional root_hash?)", v[6:38])
	t.Logf("val[38..70] = %x (1st 32B child hash)", v[38:70])
	t.Logf("(total) hashes = (val_len-6)/32 = %d (need state_mask popcount = %d)",
		(len(v)-6)/32, popcountUint16Probe(uint16(v[0])|uint16(v[1])<<8))
	c3.Close()
}

func popcountUint16Probe(x uint16) int {
	count := 0
	for x != 0 {
		count += int(x & 1)
		x >>= 1
	}
	return count
}

