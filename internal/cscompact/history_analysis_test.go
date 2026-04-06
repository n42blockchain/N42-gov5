package cscompact

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// TestAnalyzeAccountHistory profiles account history modification patterns.
func TestAnalyzeAccountHistory(t *testing.T) {
	db := openRethRO(t)
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

func TestAnalyzeStorageHistory(t *testing.T) {
	// Use Reth DB which has real history data (Erigon data is incomplete).
	db := openRethRO(t)
	defer db.Close()

	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	result, err := AnalyzeStorageHistory(tx, 1_000)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("=== Storage History (sampled %d keys) ===", result.TotalKeys)
	t.Logf("  Single-write (1x):   %6d  (%5.1f%%)  %6.1f MiB  <- NEVER revisited",
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

	// Key metric: if single-write is >50%, huge optimization opportunity.
	swPct := float64(result.SingleWrite) / float64(result.TotalKeys) * 100
	if swPct > 50 {
		t.Logf("\n  >>> %.0f%% of storage slots are single-write!", swPct)
		t.Logf("  >>> These can be replaced with simple (key → blockNum) lookup")
		t.Logf("  >>> Expected savings: %.1f MiB of bitmap data",
			float64(result.SingleWriteBytes)/1048576)
	}
}
