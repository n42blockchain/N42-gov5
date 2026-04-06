package cscompact

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// TestAnalyzeAccountHistory profiles account history modification patterns.
func TestAnalyzeAccountHistory(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	result, err := AnalyzeAccountHistory(tx, 100_000)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("=== Account History (sampled %d keys) ===", result.TotalKeys)
	t.Logf("  Single-write (1x):   %6d  (%5.1f%%)  %6.1f MiB",
		result.SingleWrite,
		float64(result.SingleWrite)/float64(result.TotalKeys)*100,
		float64(result.SingleWriteBytes)/1048576)
	t.Logf("  Few writes (2-5x):   %6d  (%5.1f%%)  %6.1f MiB",
		result.FewWrites,
		float64(result.FewWrites)/float64(result.TotalKeys)*100,
		float64(result.FewWritesBytes)/1048576)
	t.Logf("  Med writes (6-20x):  %6d  (%5.1f%%)",
		result.MedWrites,
		float64(result.MedWrites)/float64(result.TotalKeys)*100)
	t.Logf("  Many writes (21-100):%6d  (%5.1f%%)",
		result.ManyWrites,
		float64(result.ManyWrites)/float64(result.TotalKeys)*100)
	t.Logf("  Hot keys (100+):     %6d  (%5.1f%%)  %6.1f MiB",
		result.HotKeys,
		float64(result.HotKeys)/float64(result.TotalKeys)*100,
		float64(result.HotKeysBytes)/1048576)
	t.Logf("  Avg bits/key: %.1f", float64(result.TotalBitmapBits)/float64(result.TotalKeys))

	t.Logf("\n  Top 10 hottest keys:")
	for i, k := range result.TopKeys {
		if i >= 10 {
			break
		}
		keyHex := ""
		if len(k.Key) >= 6 {
			keyHex = fmt.Sprintf("%x", k.Key[:6])
		}
		t.Logf("    #%d: %s... count=%d bytes=%d", i+1, keyHex, k.Count, k.Bytes)
	}
}

// TestAnalyzeStorageHistory profiles storage history patterns.
// KEY QUESTION: what % of storage slots are single-write (set once, never changed)?
func openRethRO(t *testing.T) kv.RoDB {
	t.Helper()
	rethDB := `d:\reth2k\db`
	if _, err := os.Stat(rethDB); err != nil {
		t.Skip("Reth data not found")
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(rethDB).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestDetailedHistoryDistribution samples 1M keys with granular distribution.
func TestDetailedHistoryDistribution(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, cfg := range []struct {
		name      string
		table     string
		prefixLen int
		sample    uint64
	}{
		{"AccountHistory", "AccountHistory", 20, 1_000_000},
		{"StorageHistory", "StorageHistory", 52, 1_000_000},
	} {
		t.Logf("\n=== %s (sampling %d keys) ===", cfg.name, cfg.sample)

		cursor, err := tx.Cursor(cfg.table)
		if err != nil {
			t.Logf("  error: %v", err)
			continue
		}

		// Distribution buckets: exact counts 1-10, then ranges.
		var dist [11]uint64 // dist[1]=1x, dist[2]=2x, ..., dist[10]=10x
		var range11_20, range21_50, range51_100, range101_1000, range1000plus uint64
		var totalKeys, totalBits, totalBytes uint64
		var bytesPerBucket [11]uint64
		var bytes11_20, bytes101plus uint64

		var lastPrefix string
		var curBits, curBytes uint64

		flush := func() {
			if totalKeys == 0 && curBits == 0 {
				return
			}
			totalKeys++
			totalBits += curBits
			totalBytes += curBytes

			switch {
			case curBits <= 10:
				dist[curBits]++
				bytesPerBucket[curBits] += curBytes
			case curBits <= 20:
				range11_20++
				bytes11_20 += curBytes
			case curBits <= 50:
				range21_50++
			case curBits <= 100:
				range51_100++
			case curBits <= 1000:
				range101_1000++
				bytes101plus += curBytes
			default:
				range1000plus++
				bytes101plus += curBytes
			}
		}

		var cursorEntries uint64
		for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				break
			}
			cursorEntries++
			if len(k) < cfg.prefixLen+8 {
				continue
			}

			prefix := string(k[:cfg.prefixLen])
			if prefix != lastPrefix {
				flush()
				lastPrefix = prefix
				curBits = 0
				curBytes = 0

				if totalKeys >= cfg.sample {
					break
				}
				if totalKeys > 0 && totalKeys%100000 == 0 {
					t.Logf("  progress: %d keys, %d cursor entries", totalKeys, cursorEntries)
				}
			}

			// Count block numbers in this shard's bitmap.
			bm := roaring64.New()
			if _, err := bm.ReadFrom(bytes.NewReader(v)); err == nil {
				curBits += bm.GetCardinality()
			} else {
				curBits += uint64(len(v)) / 4
			}
			curBytes += uint64(len(k) + len(v))
		}
		flush() // last key
		cursor.Close()

		if totalKeys == 0 {
			t.Logf("  no keys found")
			continue
		}

		// Print distribution.
		t.Logf("  Total keys: %d  Total bits: %d  Total bytes: %.1f MiB",
			totalKeys, totalBits, float64(totalBytes)/1048576)
		t.Logf("  Avg bits/key: %.1f  Avg bytes/key: %.0f",
			float64(totalBits)/float64(totalKeys),
			float64(totalBytes)/float64(totalKeys))
		t.Logf("")
		t.Logf("  Writes | Count     | Pct     | Bytes      | Bytes/key")
		t.Logf("  -------|-----------|---------|------------|----------")
		for i := 1; i <= 10; i++ {
			if dist[i] > 0 {
				avg := float64(0)
				if dist[i] > 0 {
					avg = float64(bytesPerBucket[i]) / float64(dist[i])
				}
				t.Logf("  %5dx  | %9d | %5.1f%% | %8.1f KB | %5.0f B",
					i, dist[i],
					float64(dist[i])/float64(totalKeys)*100,
					float64(bytesPerBucket[i])/1024,
					avg)
			}
		}
		if range11_20 > 0 {
			t.Logf("  11-20x | %9d | %5.1f%% | %8.1f KB |",
				range11_20, float64(range11_20)/float64(totalKeys)*100,
				float64(bytes11_20)/1024)
		}
		if range21_50 > 0 {
			t.Logf("  21-50x | %9d | %5.1f%% |",
				range21_50, float64(range21_50)/float64(totalKeys)*100)
		}
		if range51_100 > 0 {
			t.Logf(" 51-100x | %9d | %5.1f%% |",
				range51_100, float64(range51_100)/float64(totalKeys)*100)
		}
		if range101_1000 > 0 {
			t.Logf("101-1Kx  | %9d | %5.1f%% | %8.1f KB |",
				range101_1000, float64(range101_1000)/float64(totalKeys)*100,
				float64(bytes101plus)/1024)
		}
		if range1000plus > 0 {
			t.Logf("  1000+x | %9d | %5.1f%% |",
				range1000plus, float64(range1000plus)/float64(totalKeys)*100)
		}

		// Compression analysis.
		t.Logf("")
		t.Logf("  === Compression analysis ===")

		// Current Roaring: totalBytes
		// Optimal varint: each block number as delta-varint from previous
		optimalVarint := uint64(0)
		for i := 1; i <= 10; i++ {
			optimalVarint += dist[i] * (1 + uint64(i)*3) // 1B count + ~3B per varint
		}
		optimalVarint += range11_20 * 50   // rough
		optimalVarint += range21_50 * 120
		optimalVarint += range51_100 * 250
		optimalVarint += range101_1000 * 2000
		optimalVarint += range1000plus * 20000

		// Single-blocknum for 1x keys: just 4 bytes
		optimal1x := dist[1] * 4

		t.Logf("  Current (Roaring):     %8.1f MiB", float64(totalBytes)/1048576)
		t.Logf("  Varint delta lists:    %8.1f MiB (%.1fx)",
			float64(optimalVarint)/1048576,
			float64(totalBytes)/float64(optimalVarint+1))
		t.Logf("  1x keys as blockNum:   %8.1f KiB (from %.1f KiB = %.0fx)",
			float64(optimal1x)/1024,
			float64(bytesPerBucket[1])/1024,
			float64(bytesPerBucket[1])/float64(optimal1x+1))
	}
}
