// reth-snapshot-export exports reth's PlainAccountState / PlainStorageState
// into compact RecSplit-indexed snapshot files.
//
// Improvements over cmd/state-to-recsplit:
//
//	1. Account values: 32B codeHash is replaced by a 3B dictionary id.
//	   The dictionary (id → 32B hash) is written as a sidecar file. About
//	   2.22M unique hashes cover all 73.9M contracts in the chain.
//	2. 4B XXHash64 key fingerprint per entry. RecSplit's MPHF returns
//	   an arbitrary ordinal for any key not in the build set; without a
//	   fingerprint a phantom-key lookup would return some other entry's
//	   bytes. The reader hashes the query key, compares against the
//	   stored fingerprint, and treats a mismatch as miss. Adds 4B/entry
//	   (~1.5 GiB on 375M storage rows). On-chain commitment still gates
//	   tampering; the fingerprint just guards against caller bugs.
//
// Output files (per table):
//
//	<prefix>.idx          — RecSplit MPHF (~1.8 bit/key)
//	<prefix>.codedict     — codeHash dictionary (accounts only)
//	                        format: [u32 count][32B hash][32B hash]...
//	                        id == position (0-based)
//	<prefix>.val          — values in MPHF-ordinal order,
//	                        [fp:4B XXH64 lo32][len:1B][value:lenB]
//	<prefix>.ef           — Elias-Fano (ordinal → byte offset in .val)
//	<prefix>.val.zst      — zstd-compressed .val (Best level)
//
// Pipeline (accounts):
//
//	Phase 0  count entries
//	Phase 1  scan values → build codeHash dictionary (extract bit-3 tails)
//	Phase 2  scan keys   → build RecSplit MPHF
//	Phase 3  scan again → for each (k,v): replace codeHash with 3B id,
//	         lookup ordinal, ETL Collect(ordinal, modified_v)
//	Phase 4  ETL Load sorted-by-ordinal → write .val + build EF
//	Phase 5  zstd compress .val → .val.zst
//
// Pipeline (storage): same minus the codeHash dict. RecSplit key is
// addr(20B) + slot(32B); stored value is the U256 part only (v[32:]).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/cespare/xxhash/v2"
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
	outDir := flag.String("out", "d:/n42-snapshot", "Output directory")
	table := flag.String("table", "", "account / storage / both (default: both)")
	countOverride := flag.Uint64("count", 0, "Skip counting, use this entry count")
	skipZstd := flag.Bool("skip-zstd", false, "Skip the final zstd compress step")
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
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["PlainAccountState"] = kv.TableCfgItem{}
			d["PlainStorageState"] = kv.TableCfgItem{}
			return d
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

	if *table == "" || *table == "account" || *table == "both" {
		dumpAccount(tx, *outDir, *countOverride, *skipZstd)
	}
	if *table == "" || *table == "storage" || *table == "both" {
		dumpStorage(tx, *outDir, *countOverride, *skipZstd)
	}
}

// extractCodeHash returns the 32B codeHash from a reth-compact account
// value, or nil if absent. Reth's compact encoding (and N42 V2) put the
// codeHash as the LAST 32B when present; the fieldBits byte uses
// different bit assignments across versions, so we don't trust it.
// Heuristic instead: len ≥ 33 AND the trailing 32B is non-zero.
// Same approach as cmd/reth-codehash-uniq, which empirically found
// 73.9M contracts / 2.22M unique hashes against this reth db.
func extractCodeHash(v []byte) []byte {
	if len(v) < 33 {
		return nil
	}
	tail := v[len(v)-32:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil
	}
	return tail
}

// rewriteAccountValue rewrites a reth-compact account value, replacing
// the trailing 32B codeHash with a 3-byte big-endian dict id. Returns
// the original slice unchanged for non-contract values (no detectable
// codeHash). Callers must not mutate the input concurrently when the
// returned slice aliases v.
func rewriteAccountValue(v []byte, dict map[[32]byte]uint32, scratch []byte) []byte {
	if len(v) < 33 {
		return v
	}
	tail := v[len(v)-32:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return v
	}
	var ch [32]byte
	copy(ch[:], tail)
	id, ok := dict[ch]
	if !ok {
		// Shouldn't happen — dict was built from the same scan. Be defensive.
		return v
	}
	prefix := v[:len(v)-32]
	scratch = append(scratch[:0], prefix...)
	scratch = append(scratch, byte(id>>16), byte(id>>8), byte(id))
	return scratch
}

func dumpAccount(tx kv.Tx, outDir string, countHint uint64, skipZstd bool) {
	const prefix = "accounts"
	const table = "PlainAccountState"

	idxPath := outDir + "/" + prefix + ".idx"
	codedictPath := outDir + "/" + prefix + ".codedict"
	valPath := outDir + "/" + prefix + ".val"
	efPath := outDir + "/" + prefix + ".ef"
	zstPath := valPath + ".zst"

	fmt.Printf("\n=== %s (table=%s) ===\n", prefix, table)
	t0 := time.Now()

	// --- Phase 0: count ---
	count := countHint
	if count == 0 {
		fmt.Printf("  [phase 0] counting entries...\n")
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor count: %v", err)
		}
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				fatal("count: %v", err)
			}
			count++
			if count%100_000_000 == 0 {
				fmt.Printf("    ... %dM\n", count/1_000_000)
			}
		}
		c.Close()
	}
	fmt.Printf("  entries: %d\n", count)

	// --- Phase 1: build codeHash dictionary ---
	fmt.Printf("  [phase 1] scanning values → codeHash dict...\n")
	t1 := time.Now()

	codeHashSet := make(map[[32]byte]struct{}, 4_000_000)
	{
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor p1: %v", err)
		}
		var n uint64
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				fatal("iter p1: %v", err)
			}
			_ = k
			if ch := extractCodeHash(v); ch != nil {
				var key [32]byte
				copy(key[:], ch)
				codeHashSet[key] = struct{}{}
			}
			n++
			if n%100_000_000 == 0 {
				fmt.Printf("    ... %dM scanned, %d unique codeHashes\n",
					n/1_000_000, len(codeHashSet))
			}
		}
		c.Close()
	}
	fmt.Printf("  unique codeHashes: %d\n", len(codeHashSet))

	// Sort hashes lexicographically and assign id = position. This gives
	// deterministic dict files for any node that runs against the same MDBX.
	codeHashList := make([][32]byte, 0, len(codeHashSet))
	for h := range codeHashSet {
		codeHashList = append(codeHashList, h)
	}
	sort.Slice(codeHashList, func(i, j int) bool {
		return bytes.Compare(codeHashList[i][:], codeHashList[j][:]) < 0
	})
	codeHashSet = nil // release memory now that we have the sorted list

	codeHashIdx := make(map[[32]byte]uint32, len(codeHashList))
	for i, h := range codeHashList {
		codeHashIdx[h] = uint32(i)
	}

	// Persist dict.
	{
		f, err := os.Create(codedictPath)
		if err != nil {
			fatal("create codedict: %v", err)
		}
		bw := bufio.NewWriterSize(f, 8<<20)
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(codeHashList)))
		bw.Write(hdr[:])
		for _, h := range codeHashList {
			bw.Write(h[:])
		}
		bw.Flush()
		f.Close()
	}
	codedictSize := fileSize(codedictPath)
	fmt.Printf("  .codedict size: %s (%d entries × 32B + 4B header)\n",
		humanBytes(codedictSize), len(codeHashList))
	fmt.Printf("  phase 1 time:   %s\n", time.Since(t1).Truncate(time.Second))

	// --- Phase 2: build RecSplit MPHF on keys (20B addr) ---
	fmt.Printf("  [phase 2] building RecSplit MPHF...\n")
	t2 := time.Now()

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

	if err := buildMPHF(rs, tx, table, false /*dupSort*/, count); err != nil {
		fatal("build MPHF: %v", err)
	}

	idxSize := fileSize(idxPath)
	fmt.Printf("  .idx size:    %s (%.2f bits/key)\n", humanBytes(idxSize),
		float64(idxSize*8)/float64(count))
	fmt.Printf("  phase 2 time: %s\n", time.Since(t2).Truncate(time.Second))

	// --- Phase 3: rewrite values + ETL by ordinal ---
	fmt.Printf("  [phase 3] rewriting values (codeHash→id) + ETL sort...\n")
	t3 := time.Now()

	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		fatal("open idx: %v", err)
	}
	defer idx.Close()
	reader := recsplit.NewIndexReader(idx)

	collector := etl.NewCollector("acct-rewrite", outDir,
		etl.NewSortableBuffer(etl.BufferOptimalSize), logger)
	defer collector.Close()

	{
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor p3: %v", err)
		}
		var n uint64
		var ordBuf [8]byte
		rewriteScratch := make([]byte, 0, 64)
		payload := make([]byte, 0, 64)
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				fatal("iter p3: %v", err)
			}
			ordinal, found := reader.Lookup(k)
			if !found {
				fatal("lookup miss for addr %x at entry %d", k, n)
			}
			binary.BigEndian.PutUint64(ordBuf[:], ordinal)

			// Pass a fresh slice over our own backing array. Don't
			// reassign rewriteScratch from the return value — for
			// non-contract entries rewriteAccountValue returns the
			// MDBX-aliased v unchanged, and using v[:0] as the next
			// scratch would have a later append write into mmap'd
			// read-only memory and segfault.
			modified := rewriteAccountValue(v, codeHashIdx, rewriteScratch[:0])

			fp := uint32(xxhash.Sum64(k))
			payload = payload[:0]
			payload = append(payload, byte(fp>>24), byte(fp>>16), byte(fp>>8), byte(fp))
			payload = append(payload, modified...)

			if err := collector.Collect(ordBuf[:], payload); err != nil {
				fatal("etl collect: %v", err)
			}
			n++
			if n%50_000_000 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				fmt.Printf("    ... %dM / %dM rewritten, heapMB=%d\n",
					n/1_000_000, count/1_000_000, m.HeapInuse>>20)
			}
		}
		c.Close()
	}
	fmt.Printf("  phase 3 time: %s\n", time.Since(t3).Truncate(time.Second))

	// --- Phase 4: ETL Load → write .val + EF ---
	fmt.Printf("  [phase 4] writing .val + EF...\n")
	t4 := time.Now()

	valFile, err := os.Create(valPath)
	if err != nil {
		fatal("create val: %v", err)
	}
	valBuf := bufio.NewWriterSize(valFile, 8<<20)

	// Estimate max .val size — V2 max ≈ 1+9+33 = 43B, plus 1B len + 4B fp = 48B per entry.
	ef := eliasfano32.NewEliasFano(count, count*48)
	var offset uint64
	var written uint64

	if err := collector.Load(nil, "", func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		ef.AddOffset(offset)
		valBuf.WriteByte(byte(len(v)))
		valBuf.Write(v)
		offset += 1 + uint64(len(v))
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
	fmt.Printf("  .val size:    %s (entry = 1B len + value)\n", humanBytes(valSize))
	fmt.Printf("  .ef size:     %s (%.2f bits/key)\n", humanBytes(efSize),
		float64(efSize*8)/float64(count))
	fmt.Printf("  phase 4 time: %s\n", time.Since(t4).Truncate(time.Second))

	// --- Phase 5: zstd compress ---
	var zstSize uint64
	if !skipZstd {
		fmt.Printf("  [phase 5] zstd compressing .val → .val.zst...\n")
		t5 := time.Now()
		compressFile(valPath, zstPath)
		zstSize = fileSize(zstPath)
		fmt.Printf("  .val.zst:     %s (%.1f%% of raw)\n", humanBytes(zstSize),
			float64(zstSize)*100/float64(valSize))
		fmt.Printf("  phase 5 time: %s\n", time.Since(t5).Truncate(time.Second))
	}

	// --- Summary ---
	totalNoZstd := idxSize + codedictSize + efSize + valSize
	totalWithZstd := idxSize + codedictSize + efSize + zstSize
	mdbxRaw := count*20 + 4_655_468_783 // empirical V2 sum
	fmt.Printf("\n  SUMMARY for %s:\n", prefix)
	fmt.Printf("    Entries:                   %d\n", count)
	fmt.Printf("    MDBX raw key+val:          %s\n", humanBytes(mdbxRaw))
	fmt.Printf("    ---\n")
	fmt.Printf("    .idx (MPHF):               %s (%.2f bits/key)\n",
		humanBytes(idxSize), float64(idxSize*8)/float64(count))
	fmt.Printf("    .codedict:                 %s\n", humanBytes(codedictSize))
	fmt.Printf("    .ef (offsets):             %s\n", humanBytes(efSize))
	fmt.Printf("    .val (uncompressed):       %s\n", humanBytes(valSize))
	if !skipZstd {
		fmt.Printf("    .val.zst (zstd):           %s (%.1f%% retained)\n",
			humanBytes(zstSize), float64(zstSize)*100/float64(valSize))
	}
	fmt.Printf("    ---\n")
	fmt.Printf("    TOTAL (no zstd):           %s (%.1f%% of MDBX raw)\n",
		humanBytes(totalNoZstd), float64(totalNoZstd)*100/float64(mdbxRaw))
	if !skipZstd {
		fmt.Printf("    TOTAL (with zstd):         %s (%.1f%% of MDBX raw)\n",
			humanBytes(totalWithZstd), float64(totalWithZstd)*100/float64(mdbxRaw))
	}
	fmt.Printf("    Total time:                %s\n",
		time.Since(t0).Truncate(time.Second))
}

func dumpStorage(tx kv.Tx, outDir string, countHint uint64, skipZstd bool) {
	const prefix = "storage"
	const table = "PlainStorageState"

	idxPath := outDir + "/" + prefix + ".idx"
	valPath := outDir + "/" + prefix + ".val"
	efPath := outDir + "/" + prefix + ".ef"
	zstPath := valPath + ".zst"

	fmt.Printf("\n=== %s (table=%s) ===\n", prefix, table)
	t0 := time.Now()

	// --- Phase 0: count ---
	count := countHint
	if count == 0 {
		fmt.Printf("  [phase 0] counting entries...\n")
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor count: %v", err)
		}
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				fatal("count: %v", err)
			}
			count++
			if count%200_000_000 == 0 {
				fmt.Printf("    ... %dM\n", count/1_000_000)
			}
		}
		c.Close()
	}
	fmt.Printf("  entries: %d\n", count)

	// --- Phase 1: build RecSplit MPHF on (addr+slot) keys ---
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

	if err := buildMPHF(rs, tx, table, true /*dupSort*/, count); err != nil {
		fatal("build MPHF: %v", err)
	}

	idxSize := fileSize(idxPath)
	fmt.Printf("  .idx size:    %s (%.2f bits/key)\n", humanBytes(idxSize),
		float64(idxSize*8)/float64(count))
	fmt.Printf("  phase 1 time: %s\n", time.Since(t1).Truncate(time.Second))

	// --- Phase 2: ETL by ordinal ---
	fmt.Printf("  [phase 2] looking up ordinals + ETL sort...\n")
	t2 := time.Now()

	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		fatal("open idx: %v", err)
	}
	defer idx.Close()
	reader := recsplit.NewIndexReader(idx)

	collector := etl.NewCollector("stor-rewrite", outDir,
		etl.NewSortableBuffer(etl.BufferOptimalSize), logger)
	defer collector.Close()

	{
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor p2: %v", err)
		}
		var n uint64
		var ordBuf [8]byte
		rsKey := make([]byte, 0, 52)
		payload := make([]byte, 0, 40)
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				fatal("iter p2: %v", err)
			}
			if len(v) < 32 {
				continue
			}
			rsKey = append(rsKey[:0], k...)
			rsKey = append(rsKey, v[:32]...)
			ordinal, found := reader.Lookup(rsKey)
			if !found {
				fatal("lookup miss for k=%x slot=%x", k, v[:32])
			}
			binary.BigEndian.PutUint64(ordBuf[:], ordinal)

			fp := uint32(xxhash.Sum64(rsKey))
			storeVal := v[32:]
			payload = payload[:0]
			payload = append(payload, byte(fp>>24), byte(fp>>16), byte(fp>>8), byte(fp))
			payload = append(payload, storeVal...)
			if err := collector.Collect(ordBuf[:], payload); err != nil {
				fatal("etl collect: %v", err)
			}
			n++
			if n%200_000_000 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				fmt.Printf("    ... %dM / %dM lookups, heapMB=%d\n",
					n/1_000_000, count/1_000_000, m.HeapInuse>>20)
			}
		}
		c.Close()
	}
	fmt.Printf("  phase 2 time: %s\n", time.Since(t2).Truncate(time.Second))

	// --- Phase 3: ETL Load → .val + EF ---
	fmt.Printf("  [phase 3] writing .val + EF...\n")
	t3 := time.Now()

	valFile, err := os.Create(valPath)
	if err != nil {
		fatal("create val: %v", err)
	}
	valBuf := bufio.NewWriterSize(valFile, 8<<20)

	// Storage value max is U256 (32B) + 1B len + 4B fp = 37B per entry.
	ef := eliasfano32.NewEliasFano(count, count*37)
	var offset uint64
	var written uint64

	if err := collector.Load(nil, "", func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		ef.AddOffset(offset)
		valBuf.WriteByte(byte(len(v)))
		valBuf.Write(v)
		offset += 1 + uint64(len(v))
		written++
		if written%200_000_000 == 0 {
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
	fmt.Printf("  .val size:    %s (entry = 1B len + value)\n", humanBytes(valSize))
	fmt.Printf("  .ef size:     %s (%.2f bits/key)\n", humanBytes(efSize),
		float64(efSize*8)/float64(count))
	fmt.Printf("  phase 3 time: %s\n", time.Since(t3).Truncate(time.Second))

	// --- Phase 4: zstd compress ---
	var zstSize uint64
	if !skipZstd {
		fmt.Printf("  [phase 4] zstd compressing .val → .val.zst...\n")
		t4 := time.Now()
		compressFile(valPath, zstPath)
		zstSize = fileSize(zstPath)
		fmt.Printf("  .val.zst:     %s (%.1f%% of raw)\n", humanBytes(zstSize),
			float64(zstSize)*100/float64(valSize))
		fmt.Printf("  phase 4 time: %s\n", time.Since(t4).Truncate(time.Second))
	}

	// --- Summary ---
	totalNoZstd := idxSize + efSize + valSize
	totalWithZstd := idxSize + efSize + zstSize
	mdbxRaw := count*20 + 17_764_108_031 // 17.78 GB measured (raw 32B slot + U256)
	mdbxRaw += count * 32                // slot bytes also in raw
	fmt.Printf("\n  SUMMARY for %s:\n", prefix)
	fmt.Printf("    Entries:                   %d\n", count)
	fmt.Printf("    MDBX raw key+val:          %s\n", humanBytes(mdbxRaw))
	fmt.Printf("    ---\n")
	fmt.Printf("    .idx (MPHF):               %s (%.2f bits/key)\n",
		humanBytes(idxSize), float64(idxSize*8)/float64(count))
	fmt.Printf("    .ef (offsets):             %s\n", humanBytes(efSize))
	fmt.Printf("    .val (uncompressed):       %s\n", humanBytes(valSize))
	if !skipZstd {
		fmt.Printf("    .val.zst (zstd):           %s (%.1f%% retained)\n",
			humanBytes(zstSize), float64(zstSize)*100/float64(valSize))
	}
	fmt.Printf("    ---\n")
	fmt.Printf("    TOTAL (no zstd):           %s (%.1f%% of MDBX raw)\n",
		humanBytes(totalNoZstd), float64(totalNoZstd)*100/float64(mdbxRaw))
	if !skipZstd {
		fmt.Printf("    TOTAL (with zstd):         %s (%.1f%% of MDBX raw)\n",
			humanBytes(totalWithZstd), float64(totalWithZstd)*100/float64(mdbxRaw))
	}
	fmt.Printf("    Total time:                %s\n",
		time.Since(t0).Truncate(time.Second))
}

// buildMPHF feeds keys from the given table into the RecSplit builder. For
// dupSort tables the key is k(20B addr) + v[:32](slot). Retries up to 10
// times on hash collision (reseeding RecSplit each retry).
func buildMPHF(rs *recsplit.RecSplit, tx kv.Tx, table string, dupSort bool, count uint64) error {
	feed := func() error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		var n uint64
		rsKey := make([]byte, 0, 52)
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			rsKey = append(rsKey[:0], k...)
			if dupSort {
				if len(v) < 32 {
					continue
				}
				rsKey = append(rsKey, v[:32]...)
			}
			if err := rs.AddKey(rsKey, n); err != nil {
				return err
			}
			n++
			if n%50_000_000 == 0 {
				fmt.Printf("    ... %dM / %dM keys fed\n",
					n/1_000_000, count/1_000_000)
			}
		}
		return nil
	}

	if err := feed(); err != nil {
		return err
	}
	if err := rs.Build(context.Background()); err != nil {
		if !rs.Collision() {
			return err
		}
		fmt.Printf("  *** hash collision detected, retrying...\n")
		for retry := 1; retry <= 10; retry++ {
			rs.ResetNextSalt()
			if err := feed(); err != nil {
				return err
			}
			if err := rs.Build(context.Background()); err != nil {
				if !rs.Collision() {
					return err
				}
				fmt.Printf("  *** retry %d: still colliding\n", retry)
				continue
			}
			fmt.Printf("  *** retry %d: success\n", retry)
			return nil
		}
		return fmt.Errorf("hash collision persists after 10 retries")
	}
	return nil
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
