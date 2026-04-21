// state-to-recsplit: dump Reth PlainAccountState / PlainStorageState into
// RecSplit-indexed flat files with fingerprint-verified values.
//
// Architecture: RecSplit MPHF maps key → ordinal. Values stored in ordinal
// order with a 4-byte key fingerprint prefix for phantom-key detection.
// Elias-Fano maps ordinal → offset in .val file.
//
// For DupSort tables (PlainStorageState), the RecSplit key is
// addr(20B) + slot_hash(32B) = 52B. The stored value is only the
// storage value (dup_value[32:]), not the slot hash.
//
// Pipeline:
//   Pass 1: iterate MDBX, build RecSplit MPHF (keys only)
//   Pass 2: iterate MDBX, lookup each key → ordinal via MPHF,
//           ETL Collect(ordinal, fp || value) for external sort
//   Pass 3: ETL Load sorted by ordinal → write .val + build EF offset table
//
// .val entry format:
//   [fingerprint:8B][len:1B][value:lenB]
//   fingerprint = Blake3(rsKey)[:8] — detects phantom keys (brute force cost 2^64)
//
// Output:
//   <prefix>.idx     — RecSplit MPHF (key → ordinal, ~3 bits/key)
//   <prefix>.val     — values in ordinal order ([fp:4B][len:1B][value]...)
//   <prefix>.ef      — Elias-Fano (ordinal → offset in .val)
//   <prefix>.val.zst — zstd max-compressed .val
//
// Usage:
//   state-to-recsplit -db d:/reth2k/db -out D:/recsplit-bench -table account
//   state-to-recsplit -db d:/reth2k/db -out D:/recsplit-bench -table storage

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"lukechampine.com/blake3"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

var logger = log.New()

func main() {
	dbPath := flag.String("db", "d:/reth2k/db", "Reth MDBX path")
	outDir := flag.String("out", "D:/recsplit-bench", "Output directory")
	table := flag.String("table", "", "account or storage")
	countOverride := flag.Uint64("count", 0, "Skip counting, use this entry count")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		fatal("mkdir: %v", err)
	}

	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Accede().
		WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
			defaults["PlainAccountState"] = kv.TableCfgItem{}
			defaults["PlainStorageState"] = kv.TableCfgItem{}
			return defaults
		}).
		Open(context.Background())
	if err != nil {
		fatal("open mdbx: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("begin tx: %v", err)
	}
	defer tx.Rollback()

	if *table == "" || *table == "account" {
		dumpTable(tx, "PlainAccountState", *outDir, "accounts", *countOverride)
	}
	if *table == "" || *table == "storage" {
		dumpTable(tx, "PlainStorageState", *outDir, "storage", *countOverride)
	}
}

// isDupSort returns true if the cursor yields duplicate keys (DupSort table).
func isDupSort(tx kv.Tx, table string) bool {
	c, err := tx.Cursor(table)
	if err != nil {
		return false
	}
	defer c.Close()
	k1, _, _ := c.First()
	if k1 == nil {
		return false
	}
	k2, _, _ := c.Next()
	return k2 != nil && bytes.Equal(k1, k2)
}

// appendRSKey appends the unique RecSplit key to dst and returns it.
// For DupSort tables: addr(20) + slot_hash(first 32B of dup value) = 52B.
// For regular tables: raw MDBX key as-is.
func appendRSKey(dst, k, v []byte, dupSort bool) []byte {
	dst = append(dst, k...)
	if dupSort && len(v) >= 32 {
		dst = append(dst, v[:32]...)
	}
	return dst
}

// makeStoreValue extracts the value to store in .val file.
// For DupSort tables: only v[32:] (the actual storage value, slot hash is in the key).
// For regular tables: full value.
func makeStoreValue(v []byte, dupSort bool) []byte {
	if dupSort && len(v) >= 32 {
		return v[32:]
	}
	return v
}

// FingerprintSize is the number of bytes used for phantom-key detection.
// 8 bytes (Blake3) → accidental collision 1/2^64, brute force O(N × 2^64).
const FingerprintSize = 8

// fingerprint returns an 8-byte Blake3 prefix for phantom-key detection.
func fingerprint(key []byte) [FingerprintSize]byte {
	h := blake3.Sum256(key)
	var fp [FingerprintSize]byte
	copy(fp[:], h[:FingerprintSize])
	return fp
}

func dumpTable(tx kv.Tx, table, outDir, prefix string, countHint uint64) {
	fmt.Printf("\n=== %s (table=%s) ===\n", prefix, table)
	t0 := time.Now()

	idxPath := outDir + "/" + prefix + ".idx"
	valPath := outDir + "/" + prefix + ".val"
	efPath := outDir + "/" + prefix + ".ef"
	zstPath := valPath + ".zst"

	dupSort := isDupSort(tx, table)
	if dupSort {
		fmt.Printf("  DupSort detected — rsKey = addr(20) + slot(32)\n")
	}

	// --- Phase 0: count ---
	count := countHint
	if count == 0 {
		fmt.Printf("  counting entries...\n")
		cursor, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor count: %v", err)
		}
		for k, _, err := cursor.First(); k != nil; k, _, err = cursor.Next() {
			if err != nil {
				fatal("count: %v", err)
			}
			count++
			if count%100_000_000 == 0 {
				fmt.Printf("    ... %dM\n", count/1_000_000)
			}
		}
		cursor.Close()
	}
	fmt.Printf("  entries: %d\n", count)

	// --- Phase 1: build RecSplit MPHF ---
	fmt.Printf("  [phase 1] building RecSplit MPHF...\n")
	t1 := time.Now()

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:    int(count),
		BucketSize:  2000,
		IndexFile:   idxPath,
		TmpDir:      outDir,
		LeafSize:    8,
		Enums:       false,
		NoValues:    true,
		EtlBufLimit: 512 * datasize.MB,
	}, logger)
	if err != nil {
		fatal("new recsplit: %v", err)
	}

	cursor, err := tx.Cursor(table)
	if err != nil {
		fatal("cursor p1: %v", err)
	}
	var n uint64
	var totalKeyBytes, totalValBytes uint64
	rsKeyBuf1 := make([]byte, 0, 52)
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fatal("iter p1: %v", err)
		}
		rsKeyBuf1 = appendRSKey(rsKeyBuf1[:0], k, v, dupSort)
		if err := rs.AddKey(rsKeyBuf1, n); err != nil {
			fatal("addkey: %v", err)
		}
		totalKeyBytes += uint64(len(k))
		totalValBytes += uint64(len(v))
		n++
		if n%50_000_000 == 0 {
			fmt.Printf("    ... %dM / %dM keys fed\n", n/1_000_000, count/1_000_000)
		}
	}
	cursor.Close()

	if err := rs.Build(context.Background()); err != nil {
		if rs.Collision() {
			fmt.Printf("  *** hash collision detected, retrying...\n")
			for retry := 1; retry <= 10; retry++ {
				rs.ResetNextSalt()
				c2, err2 := tx.Cursor(table)
				if err2 != nil {
					fatal("cursor retry: %v", err2)
				}
				var n2 uint64
				for k, v, err2 := c2.First(); k != nil; k, v, err2 = c2.Next() {
					if err2 != nil {
						fatal("iter retry: %v", err2)
					}
					if err := rs.AddKey(appendRSKey(rsKeyBuf1[:0], k, v, dupSort), n2); err != nil {
						fatal("addkey retry: %v", err)
					}
					n2++
				}
				c2.Close()
				if err := rs.Build(context.Background()); err != nil {
					if rs.Collision() {
						fmt.Printf("  *** retry %d: still colliding\n", retry)
						continue
					}
					fatal("build retry %d: %v", retry, err)
				}
				fmt.Printf("  *** retry %d: success\n", retry)
				break
			}
			if rs.Collision() {
				fatal("hash collision persists after 10 retries — likely duplicate keys")
			}
		} else {
			fatal("build: %v", err)
		}
	}
	idxSize := fileSize(idxPath)
	fmt.Printf("  .idx size:    %s (%.1f bits/key)\n", humanBytes(idxSize), float64(idxSize*8)/float64(count))
	fmt.Printf("  phase 1 time: %s\n", time.Since(t1).Truncate(time.Second))

	// --- Phase 2: lookup ordinals, ETL sort with fp+value ---
	fmt.Printf("  [phase 2] lookup ordinals + ETL sort...\n")
	t2 := time.Now()

	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		fatal("open idx: %v", err)
	}
	reader := recsplit.NewIndexReader(idx)

	collector := etl.NewCollector("recsplit-sort", outDir,
		etl.NewSortableBuffer(etl.BufferOptimalSize), logger)
	defer collector.Close()

	cursor, err = tx.Cursor(table)
	if err != nil {
		fatal("cursor p2: %v", err)
	}
	n = 0
	var ordBuf [8]byte
	// Reusable buffers to avoid 1.5B allocations.
	rsKeyBuf := make([]byte, 0, 52)
	etlValBuf := make([]byte, 0, FingerprintSize+80)
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fatal("iter p2: %v", err)
		}
		rsKeyBuf = appendRSKey(rsKeyBuf[:0], k, v, dupSort)
		ordinal, found := reader.Lookup(rsKeyBuf)
		if !found {
			fatal("lookup miss at entry %d — MPHF inconsistent", n)
		}
		binary.BigEndian.PutUint64(ordBuf[:], ordinal)

		fp := fingerprint(rsKeyBuf)
		storeVal := makeStoreValue(v, dupSort)
		etlValBuf = append(etlValBuf[:0], fp[:]...)
		etlValBuf = append(etlValBuf, storeVal...)

		if err := collector.Collect(ordBuf[:], etlValBuf); err != nil {
			fatal("etl collect: %v", err)
		}
		n++
		if n%50_000_000 == 0 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			fmt.Printf("    ... %dM / %dM lookups, heapMB=%d\n",
				n/1_000_000, count/1_000_000, m.HeapInuse>>20)
		}
	}
	cursor.Close()
	idx.Close()
	fmt.Printf("  phase 2 time: %s\n", time.Since(t2).Truncate(time.Second))

	// --- Phase 3: ETL Load → write .val (with fp) + build EF ---
	fmt.Printf("  [phase 3] writing .val + EF...\n")
	t3 := time.Now()

	valFile, err := os.Create(valPath)
	if err != nil {
		fatal("create val: %v", err)
	}
	valBuf := bufio.NewWriterSize(valFile, 8<<20)

	ef := eliasfano32.NewEliasFano(count, (uint64(FingerprintSize)+1+80)*count)
	var offset uint64
	var written uint64

	if err := collector.Load(nil, "", func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		ef.AddOffset(offset)

		fp := v[:FingerprintSize]
		storeVal := v[FingerprintSize:]
		valBuf.Write(fp)
		valBuf.WriteByte(byte(len(storeVal)))
		valBuf.Write(storeVal)
		offset += uint64(FingerprintSize) + 1 + uint64(len(storeVal))
		written++

		if written%50_000_000 == 0 {
			fmt.Printf("    ... %dM / %dM written\n", written/1_000_000, count/1_000_000)
		}
		return nil
	}, etl.TransformArgs{}); err != nil {
		fatal("etl load: %v", err)
	}

	valBuf.Flush()
	valFile.Close()
	ef.Build()

	efData := ef.AppendBytes(nil)
	if err := os.WriteFile(efPath, efData, 0644); err != nil {
		fatal("write ef: %v", err)
	}

	valSize := fileSize(valPath)
	efSize := fileSize(efPath)
	fmt.Printf("  .val size:    %s (entry = %dB fp + 1B len + value)\n", humanBytes(valSize), FingerprintSize)
	fmt.Printf("  .ef size:     %s (%.1f bits/key)\n", humanBytes(efSize), float64(efSize*8)/float64(count))
	fmt.Printf("  phase 3 time: %s\n", time.Since(t3).Truncate(time.Second))

	// --- Phase 4: zstd compress .val ---
	fmt.Printf("  [phase 4] zstd compressing .val...\n")
	t4 := time.Now()
	compressFile(valPath, zstPath)
	zstSize := fileSize(zstPath)
	fmt.Printf("  .val.zst:     %s (%.1f%% of raw)\n", humanBytes(zstSize), float64(zstSize)*100/float64(valSize))
	fmt.Printf("  zstd time:    %s\n", time.Since(t4).Truncate(time.Second))

	// --- Summary ---
	totalNew := idxSize + efSize + zstSize
	mdbxDataSize := totalKeyBytes + totalValBytes
	fmt.Printf("\n  SUMMARY for %s:\n", prefix)
	fmt.Printf("    Entries:                   %d\n", count)
	fmt.Printf("    MDBX keys+vals (raw):      %s\n", humanBytes(mdbxDataSize))
	fmt.Printf("    MDBX table on disk:        %s (with B+tree overhead)\n", humanBytes(mdbxTableSize(table)))
	fmt.Printf("    ---\n")
	fmt.Printf("    RecSplit MPHF (.idx):      %s (%.1f bits/key)\n", humanBytes(idxSize), float64(idxSize*8)/float64(count))
	fmt.Printf("    Elias-Fano offsets (.ef):   %s (%.1f bits/key)\n", humanBytes(efSize), float64(efSize*8)/float64(count))
	fmt.Printf("    Values+fp zstd (.val.zst): %s\n", humanBytes(zstSize))
	fmt.Printf("    NEW TOTAL (idx+ef+zst):    %s\n", humanBytes(totalNew))
	fmt.Printf("    ---\n")
	fmt.Printf("    fp overhead:               %s (%d × %dB)\n", humanBytes(count*uint64(FingerprintSize)), count, FingerprintSize)
	fmt.Printf("    vs MDBX data:              %.1f%% savings\n", (1-float64(totalNew)/float64(mdbxDataSize))*100)
	fmt.Printf("    vs MDBX table:             %.1f%% savings\n", (1-float64(totalNew)/float64(mdbxTableSize(table)))*100)
	fmt.Printf("    Total time:                %s\n", time.Since(t0).Truncate(time.Second))
}

func compressFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		fatal("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		fatal("create %s: %v", dst, err)
	}
	outBuf := bufio.NewWriterSize(out, 8<<20)
	enc, err := zstd.NewWriter(outBuf,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(8<<20),
	)
	if err != nil {
		fatal("zstd encoder: %v", err)
	}
	buf := make([]byte, 4<<20)
	if _, err := io.CopyBuffer(enc, in, buf); err != nil {
		fatal("zstd compress: %v", err)
	}
	enc.Close()
	outBuf.Flush()
	out.Close()
}

func mdbxTableSize(table string) uint64 {
	switch table {
	case "PlainAccountState":
		return 22_4 * (1 << 30) / 10
	case "PlainStorageState":
		return 126_9 * (1 << 30) / 10
	default:
		return 1
	}
}

func fileSize(path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return uint64(fi.Size())
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
