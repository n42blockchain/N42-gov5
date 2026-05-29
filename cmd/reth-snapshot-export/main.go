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
	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

var logger = log.New()

// limitEntries, when >0, caps the number of entries exported per table — used
// to generate small test snapshots quickly. 0 = export the whole table.
var limitEntries uint64

func main() {
	dbPath := flag.String("db", "d:/reth2k/db", "MDBX path (Reth or N42)")
	outDir := flag.String("out", "d:/n42-snapshot", "Output directory")
	table := flag.String("table", "", "account / storage / both (default: both)")
	accountTable := flag.String("account-table", "PlainAccountState", "MDBX table for accounts (N42: Account)")
	storageTable := flag.String("storage-table", "PlainStorageState", "MDBX table for storage (N42: Storage; dup-sort key=20B addr, value=slot(32B)+val)")
	n42 := flag.Bool("n42", false, "Shortcut: sets account-table=Account, storage-table=Storage")
	countOverride := flag.Uint64("count", 0, "Skip counting, use this entry count")
	skipZstd := flag.Bool("skip-zstd", false, "Skip the final zstd compress step")
	endBlock := flag.Uint64("end-block", 0, "Snapshot end block; when > 0, files are named accounts.0-<endBlock>.* / storage.0-<endBlock>.* (H.3 segment naming, monolithic case)")
	limit := flag.Uint64("limit", 0, "Cap entries per table (for small test snapshots; 0 = export all)")
	flag.Parse()
	limitEntries = *limit

	if *n42 {
		*accountTable = "Account"
		*storageTable = "Storage"
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		fatal("mkdir: %v", err)
	}

	accTbl, stoTbl := *accountTable, *storageTable
	prefAcc, prefSto := "accounts", "storage"
	if *endBlock > 0 {
		prefAcc = fmt.Sprintf("accounts.0-%d", *endBlock)
		prefSto = fmt.Sprintf("storage.0-%d", *endBlock)
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
			d[accTbl] = kv.TableCfgItem{}
			d[stoTbl] = kv.TableCfgItem{}
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
		dumpAccount(tx, accTbl, *outDir, prefAcc, *countOverride, *skipZstd)
	}
	if *table == "" || *table == "storage" || *table == "both" {
		dumpStorage(tx, stoTbl, *outDir, prefSto, *countOverride, *skipZstd)
	}
}

// extractCodeHash returns the 32B codeHash from a reth-compact account
// value, or nil if absent. Reth's compact encoding (and N42 V2) put the
// codeHash as the LAST 32B when present; the fieldBits byte uses
// different bit assignments across versions, so we don't trust it.
// Heuristic instead: len ≥ 33 AND the trailing 32B is non-zero.
// Same approach as cmd/reth-codehash-uniq, which empirically found
// 73.9M contracts / 2.22M unique hashes against this reth db.
// decodeRethCompact parses a reth Account Compact value.
//
// Layout: [2B flags LE][nonce: nonceLen B BE][balance: balLen B BE][codeHash: 32B if present]
//
//	flags: nonceLen   = bits[0:4]   (u64, 0..8 bytes, leading-zero trimmed)
//	       balanceLen = bits[4:10]  (U256, 0..32 bytes, leading-zero trimmed)
//	       hasCode    = bit[10]     (bytecode_hash Option present)
//
// reth never stores Some(emptyCodeHash) — EOAs are None — so hasCode == true
// means a real contract codeHash. ok==false on a malformed length.
func decodeRethCompact(v []byte) (nonce uint64, balance uint256.Int, codeHash [32]byte, hasCode, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	hasCode = (flags>>10)&1 == 1
	need := 2 + nonceLen + balLen
	if hasCode {
		need += 32
	}
	if balLen > 32 || nonceLen > 8 || len(v) != need {
		hasCode = false
		return
	}
	p := 2
	if nonceLen > 0 {
		var nb [8]byte
		copy(nb[8-nonceLen:], v[p:p+nonceLen])
		nonce = binary.BigEndian.Uint64(nb[:])
	}
	p += nonceLen
	if balLen > 0 {
		var bb [32]byte
		copy(bb[32-balLen:], v[p:p+balLen])
		balance.SetBytes(bb[:])
	}
	p += balLen
	if hasCode {
		copy(codeHash[:], v[p:p+32])
	}
	return nonce, balance, codeHash, hasCode, true
}

// extractCodeHash returns the 32B contract codeHash of a reth-compact account,
// or nil for an EOA (no bytecode_hash). Uses the flags bit — NOT a trailing-32B
// heuristic, which would mis-read a large EOA balance as a codeHash.
func extractCodeHash(v []byte) []byte {
	_, _, ch, hasCode, ok := decodeRethCompact(v)
	if !ok || !hasCode {
		return nil
	}
	out := make([]byte, 32)
	copy(out, ch[:])
	return out
}

// rewriteAccountValue converts a reth-compact account value into the N42 V2
// account encoding (see common/account.EncodeForStorageV2) with the 32B
// codeHash replaced by a 3-byte big-endian codedict id. Output layout:
//
//	[fieldBits:1B][nonce:uvarint][balance:1B len + trimmed BE][codeHash id:3B]
//	fieldBits bit0=nonce bit1=balance bit3=codeHash (matches snapshotreader.DecodeAccount)
//
// The returned slice is backed by scratch (never aliases v); callers copy it
// before the next call.
func rewriteAccountValue(v []byte, dict map[[32]byte]uint32, scratch []byte) []byte {
	nonce, balance, codeHash, hasCode, ok := decodeRethCompact(v)
	if !ok {
		fatal("malformed reth-compact account value (len=%d): %x", len(v), v)
	}
	scratch = append(scratch[:0], 0) // fieldBits placeholder at [0]
	var fieldBits byte
	if nonce > 0 {
		fieldBits |= 1
		var tmp [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(tmp[:], nonce)
		scratch = append(scratch, tmp[:n]...)
	}
	if !balance.IsZero() {
		fieldBits |= 2
		bb := balance.Bytes32()
		start := 0
		for start < 31 && bb[start] == 0 {
			start++
		}
		scratch = append(scratch, byte(32-start))
		scratch = append(scratch, bb[start:]...)
	}
	if hasCode {
		id, found := dict[codeHash]
		if !found {
			fatal("codeHash %x not in dict (built from same scan)", codeHash)
		}
		fieldBits |= 8
		scratch = append(scratch, byte(id>>16), byte(id>>8), byte(id))
	}
	scratch[0] = fieldBits
	return scratch
}

func dumpAccount(tx kv.Tx, table, outDir, prefix string, countHint uint64, skipZstd bool) {
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
			if limitEntries > 0 && count >= limitEntries {
				break
			}
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
			if limitEntries > 0 && n >= limitEntries {
				break
			}
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

			// rewriteAccountValue always writes into our own scratch
			// (never aliases the MDBX-mapped v); payload copies it below
			// before the next iteration reuses the buffer.
			modified := rewriteAccountValue(v, codeHashIdx, rewriteScratch[:0])

			fp := uint32(xxhash.Sum64(k))
			payload = payload[:0]
			payload = append(payload, byte(fp>>24), byte(fp>>16), byte(fp>>8), byte(fp))
			payload = append(payload, modified...)

			if err := collector.Collect(ordBuf[:], payload); err != nil {
				fatal("etl collect: %v", err)
			}
			n++
			if limitEntries > 0 && n >= limitEntries {
				break
			}
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

func dumpStorage(tx kv.Tx, table, outDir, prefix string, countHint uint64, skipZstd bool) {
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
			if limitEntries > 0 && count >= limitEntries {
				break
			}
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
			if limitEntries > 0 && n >= limitEntries {
				break
			}
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
			if limitEntries > 0 && n >= limitEntries {
				break
			}
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
