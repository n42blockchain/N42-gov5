// n42-trie-compress: spike to measure how well reth's persistent
// trie tables (AccountsTrie, StoragesTrie) compress under N42's
// MPHF+fp coldstore format (same scheme used for state history at
// D:\n42-history-full — ~17 B/entry empirical on storage history).
//
// Read path: reth DB → cursor scan (table → key, value) → ETL collect.
// Write path: MPHFWriter (page 64, zstd, RecSplit MPHF, 4B xxhash fp).
//
// This is opt-in / one-shot: tool runs against a stopped reth (or any
// readonly DB) and writes <out>/<prefix>.{mphf,idx,kv}. Caller decides
// what to do with the compressed dir (currently: keep alongside, use
// for cold reads, the original MDBX stays as source-of-truth until we
// land a Reader that proves equivalence).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

func main() {
	dbPath := flag.String("db", `D:\reth2k\db`, "source reth/erigon MDBX dir (readonly)")
	table := flag.String("table", "AccountsTrie", "source table (AccountsTrie | StoragesTrie)")
	out := flag.String("out", "", "output dir for <prefix>.mphf/idx/kv (required)")
	prefix := flag.String("prefix", "", "file prefix; default = lowercased table")
	pageSize := flag.Int("page", 64, "entries per kv page")
	tmpDir := flag.String("tmp", "", "ETL spill dir (default <out>/etl-tmp)")
	mapSizeGB := flag.Int("mapsize-gb", 4096, "source DB mapsize cap")
	keyCount := flag.Int("key-count", 0, "expected key count (0 = use BucketStat first)")
	etlBufMB := flag.Uint64("etl-buf-mb", 2048, "ETL buffer size MB")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-trie-compress --db <reth-dir> --table AccountsTrie --out <dir> [--prefix name]")
		os.Exit(1)
	}
	if *prefix == "" {
		switch *table {
		case "AccountsTrie":
			*prefix = "accountstrie"
		case "StoragesTrie":
			*prefix = "storagestrie"
		default:
			*prefix = *table
		}
	}
	if *tmpDir == "" {
		*tmpDir = *out + "/etl-tmp"
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal("mkdir out: %v", err)
	}
	if err := os.MkdirAll(*tmpDir, 0o755); err != nil {
		fatal("mkdir tmp: %v", err)
	}

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(*table)).
		Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("tx: %v", err)
	}
	defer tx.Rollback()

	// Probe entries count if caller didn't supply --key-count.
	mtx := tx.(*mdbxkv.MdbxTx)
	if *keyCount <= 0 {
		st, err := mtx.BucketStat(*table)
		if err != nil {
			fatal("stat %s: %v", *table, err)
		}
		*keyCount = int(st.Entries)
		fmt.Fprintf(os.Stderr, "source %s entries=%d\n", *table, *keyCount)
	}
	if *keyCount <= 0 {
		fatal("empty source table %s", *table)
	}

	w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir:  *out,
		Prefix:   *prefix,
		PageSize: *pageSize,
		TmpDir:   *tmpDir,
		KeyCount: *keyCount,
		EtlBufMB: *etlBufMB,
		Logger:   logger,
	})
	if err != nil {
		fatal("MPHFWriter: %v", err)
	}

	c, err := tx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	var (
		appended    uint64
		bytesIn     uint64
		lastLog     = time.Now()
		logInterval = 5 * time.Second
	)
	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		// MDBX returns shared buffers; MPHFWriter copies via ETL.Collect
		// internally, so direct pass is safe.
		if appendErr := w.Append(k, v); appendErr != nil {
			fatal("append #%d: %v", appended, appendErr)
		}
		appended++
		bytesIn += uint64(len(k) + len(v))
		if time.Since(lastLog) >= logInterval {
			pct := 100 * float64(appended) / float64(*keyCount)
			fmt.Fprintf(os.Stderr, "  appended %d/%d (%.1f%%)  raw=%.2f GB  rate=%.0f k/s\n",
				appended, *keyCount, pct,
				float64(bytesIn)/1e9,
				float64(appended)/time.Since(t0).Seconds()/1000)
			lastLog = time.Now()
		}
	}

	fmt.Fprintf(os.Stderr, "scan done: %d entries, %.2f GB raw, %.1f s\n",
		appended, float64(bytesIn)/1e9, time.Since(t0).Seconds())

	closeT0 := time.Now()
	if err := w.Close(); err != nil {
		fatal("MPHFWriter.Close: %v", err)
	}
	fmt.Fprintf(os.Stderr, "MPHF build/pack: %s\n", time.Since(closeT0).Truncate(time.Second))

	// Final report.
	stats := w.Stats()
	totalOut := stats.KvSize + stats.IdxSize + stats.MphfSize
	fmt.Println()
	fmt.Printf("=== n42-trie-compress complete ===\n")
	fmt.Printf("  source table       %s\n", *table)
	fmt.Printf("  entries            %d\n", stats.KeyCount)
	fmt.Printf("  pages              %d\n", stats.PageCount)
	fmt.Printf("  raw in             %.2f GB\n", float64(bytesIn)/1e9)
	fmt.Printf("  out .kv            %.2f GB\n", float64(stats.KvSize)/1e9)
	fmt.Printf("  out .idx           %.2f MB\n", float64(stats.IdxSize)/1e6)
	fmt.Printf("  out .mphf          %.2f MB\n", float64(stats.MphfSize)/1e6)
	fmt.Printf("  out total          %.2f GB\n", float64(totalOut)/1e9)
	if bytesIn > 0 {
		fmt.Printf("  compression ratio  %.2fx  (%.1f%% saved)\n",
			float64(bytesIn)/float64(totalOut),
			100*(1-float64(totalOut)/float64(bytesIn)))
	}
	fmt.Printf("  total elapsed      %s\n", time.Since(t0).Truncate(time.Second))
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
