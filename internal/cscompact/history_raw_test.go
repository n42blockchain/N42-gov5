package cscompact

import (
	"encoding/hex"
	"fmt"
	"testing"
)

// TestHistoryRawDump dumps first 20 entries from history tables to understand format.
func TestHistoryRawDump(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, table := range []string{"AccountsHistory", "StoragesHistory"} {
		t.Logf("\n=== %s (first 20 entries) ===", table)
		cursor, err := tx.Cursor(table)
		if err != nil {
			t.Logf("  error: %v", err)
			continue
		}

		count := 0
		for k, v, err := cursor.First(); k != nil && count < 20; k, v, err = cursor.Next() {
			if err != nil {
				break
			}
			t.Logf("  key[%d]=%s val[%d]=%s",
				len(k), hex.EncodeToString(k[:min(len(k), 40)]),
				len(v), truncHex(v, 40))
			count++
		}
		cursor.Close()

		// Count total entries.
		cursor2, _ := tx.Cursor(table)
		total := 0
		for k, _, _ := cursor2.First(); k != nil; k, _, _ = cursor2.Next() {
			total++
			if total > 10_000_000 {
				break
			}
		}
		cursor2.Close()
		t.Logf("  total entries (up to 10M): %d", total)
	}
}

func truncHex(b []byte, maxBytes int) string {
	if len(b) > maxBytes {
		return hex.EncodeToString(b[:maxBytes]) + fmt.Sprintf("...(%dB)", len(b))
	}
	return hex.EncodeToString(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
