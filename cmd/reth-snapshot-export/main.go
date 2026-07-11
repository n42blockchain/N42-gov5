// reth-snapshot-export exports reth's PlainAccountState / PlainStorageState
// into compact RecSplit-indexed snapshot files.
//
// v2 pipeline (sharded, single-scan, slot-order .val):
//
//	Phase A  ONE sequential MDBX scan. Each row is routed to a shard by the
//	         top bits of xxhash64(key); its entry is appended to that shard's
//	         SCAN-ORDER temp .val, and (key, scanOffset) is spilled to a
//	         per-shard keyfile. Accounts also build the codeHash dictionary
//	         on the fly (first-seen order — deterministic for a given MDBX).
//	Phase B  Per shard, in PARALLEL: build a NoValues RecSplit MPHF from the
//	         keyfile, then rewrite the temp .val into MPHF-SLOT order in the
//	         final .val and emit the slot→offset Elias-Fano .ef. Because the
//	         final .val is slot-ordered, offsets are monotonic in slot and a
//	         compact EF suffices — there is NO per-key record array. This is
//	         the v1 on-disk layout, produced from a single scan and in
//	         parallel. Collisions retry by replaying the keyfile.
//	Phase C  Per-shard zstd compress .val → .val.zst.
//
// Compared to v1 (4 full-table scans + a single-threaded 17 GB ETL re-sort
// for the storage table), v2 touches the source table exactly once and does
// the ordinal re-sort per-shard/in-memory/parallel, so it keeps v1's compact
// ~9 bit/key index while removing the ">60 GB page-cache window" requirement.
//
// Output files (per table, N = --shards):
//
//	<prefix>.s00..s<N-1>.idx      — NoValues RecSplit MPHF (Lookup→slot)
//	<prefix>.s00..s<N-1>.ef       — Elias-Fano slot→byte-offset into .val
//	<prefix>.s00..s<N-1>.val      — entries in MPHF-slot order,
//	                                [len:1B][fp:fpBytes XXH64][value]
//	<prefix>.s00..s<N-1>.val.zst  — zstd-compressed .val
//	<prefix>.codedict             — codeHash dictionary (accounts only),
//	                                [u32 count][32B hash]... id == position
//
// Shard routing: shard = xxhash64(key) >> (64 - log2(N)). The same hash's
// low fpBytes*8 bits are the per-entry fingerprint (phantom-key guard: the
// MPHF returns an arbitrary slot for any key not in the build set).
//
// Account values: the 32B codeHash is replaced by a 3B dictionary id, and
// the reth-compact encoding is rewritten to N42 V2 (see rewriteAccountValue).
// Storage: RecSplit key is addr(20B) + slot(32B); value is the trimmed U256.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math/bits"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/cespare/xxhash/v2"
	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/mmap"
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
	countOverride := flag.Uint64("count", 0, "Display-only entry count override (v2 derives exact per-shard counts from the scan)")
	skipZstd := flag.Bool("skip-zstd", false, "Skip the final zstd compress step")
	endBlock := flag.Uint64("end-block", 0, "Snapshot end block; when > 0, files are named accounts.0-<endBlock>.* / storage.0-<endBlock>.* (H.3 segment naming, monolithic case)")
	limit := flag.Uint64("limit", 0, "Cap entries per table (for small test snapshots; 0 = export all)")
	shards := flag.Int("shards", 16, "Number of hash shards (power of two; per-shard MPHFs build in parallel)")
	flag.Parse()
	limitEntries = *limit

	if *shards < 1 || *shards > 256 || bits.OnesCount(uint(*shards)) != 1 {
		fatal("--shards must be a power of two in [1,256], got %d", *shards)
	}
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
		dumpTable(tx, accTbl, *outDir, prefAcc, true, *shards, *countOverride, *skipZstd)
	}
	if *table == "" || *table == "storage" || *table == "both" {
		dumpTable(tx, stoTbl, *outDir, prefSto, false, *shards, *countOverride, *skipZstd)
	}
}

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

// codeDict assigns 3-byte ids to codeHashes in first-seen scan order. For a
// given MDBX the scan order is fixed, so the dict file stays deterministic
// across runs (v1 sorted lexicographically instead; readers only ever treat
// the dict as an id→hash array, so the order change is invisible to them).
type codeDict struct {
	idx  map[[32]byte]uint32
	list [][32]byte
}

func newCodeDict() *codeDict {
	return &codeDict{idx: make(map[[32]byte]uint32, 4_000_000)}
}

func (d *codeDict) id(h [32]byte) uint32 {
	if id, ok := d.idx[h]; ok {
		return id
	}
	id := uint32(len(d.list))
	if id >= 1<<24 {
		fatal("codeHash dict overflow: more than 2^24 unique hashes")
	}
	d.idx[h] = id
	d.list = append(d.list, h)
	return id
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
func rewriteAccountValue(v []byte, dict *codeDict, scratch []byte) []byte {
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
		id := dict.id(codeHash)
		fieldBits |= 8
		scratch = append(scratch, byte(id>>16), byte(id>>8), byte(id))
	}
	scratch[0] = fieldBits
	return scratch
}

// fpBytes is the per-entry key-fingerprint width. Kept at 4 (32-bit): a
// phantom lookup (MPHF returns an ordinal for a key not in the set — e.g. a
// zero storage slot) is rejected iff the fingerprint mismatches, so the
// false-positive rate is 2^-32/lookup. 3 bytes (2^-24) is NOT safe here:
// a 100k-block catch-up issues ~5e8 zero-slot reads, which at 2^-24 yields
// ~30 wrong values → state-root mismatches. Do not shrink without changing
// how phantoms are rejected.
const fpBytes = 4

// shardOut owns one shard's files. Phase A appends every entry to a
// SCAN-ORDER temp .val (valScanPath) and logs (key, scanOffset) to a keyfile.
// Phase B builds the shard's NoValues MPHF, then rewrites the entries into
// MPHF-slot order in the final .val (so offsets are monotonic in slot and a
// compact Elias-Fano .ef suffices — no per-key record array, matching the v1
// on-disk layout). This keeps v2's single-source-scan + parallel build while
// recovering v1's ~9 bit/key index size.
type shardOut struct {
	idxPath, efPath, valPath    string
	valScanPath, keyPath        string
	valScanF, keyF              *os.File
	valScanW, keyW              *bufio.Writer
	scanOffset                  uint64
	count                       uint64
}

func newShardOut(outDir, prefix string, s int) *shardOut {
	base := fmt.Sprintf("%s/%s.s%02d", outDir, prefix, s)
	sh := &shardOut{
		idxPath:     base + ".idx",
		efPath:      base + ".ef",
		valPath:     base + ".val",
		valScanPath: base + ".val.scan.tmp",
		keyPath:     base + ".keys.tmp",
	}
	var err error
	if sh.valScanF, err = os.Create(sh.valScanPath); err != nil {
		fatal("create %s: %v", sh.valScanPath, err)
	}
	if sh.keyF, err = os.Create(sh.keyPath); err != nil {
		fatal("create %s: %v", sh.keyPath, err)
	}
	sh.valScanW = bufio.NewWriterSize(sh.valScanF, 8<<20)
	sh.keyW = bufio.NewWriterSize(sh.keyF, 8<<20)
	return sh
}

// add appends one scan-order entry [len:1B][fp:fpBytes][value] to the temp
// .val and records [key][scanOffsetBE:8B] in the keyfile (scanOffset points at
// the entry's len byte).
func (sh *shardOut) add(key []byte, fp uint32, value []byte) {
	payloadLen := fpBytes + len(value)
	if payloadLen > 255 {
		fatal("value too long for 1B length: %d", payloadLen)
	}
	var rec [8]byte
	binary.BigEndian.PutUint64(rec[:], sh.scanOffset)
	sh.keyW.Write(key)
	sh.keyW.Write(rec[:])
	sh.valScanW.WriteByte(byte(payloadLen))
	var fpb [4]byte
	binary.BigEndian.PutUint32(fpb[:], fp)
	sh.valScanW.Write(fpb[4-fpBytes:])
	sh.valScanW.Write(value)
	sh.scanOffset += 1 + uint64(payloadLen)
	sh.count++
}

func (sh *shardOut) finishWriters() {
	if err := sh.valScanW.Flush(); err != nil {
		fatal("flush scan val: %v", err)
	}
	if err := sh.valScanF.Close(); err != nil {
		fatal("close scan val: %v", err)
	}
	if err := sh.keyW.Flush(); err != nil {
		fatal("flush keys: %v", err)
	}
	if err := sh.keyF.Close(); err != nil {
		fatal("close keys: %v", err)
	}
}

func dumpTable(tx kv.Tx, table, outDir, prefix string, accounts bool, nShards int, countHint uint64, skipZstd bool) {
	fmt.Printf("\n=== %s (table=%s, shards=%d) ===\n", prefix, table, nShards)
	t0 := time.Now()

	// Row count from MDBX B-tree stat (O(1); ms_entries counts every dup item
	// on DupSort tables). Display/progress only — RecSplit gets exact
	// per-shard counts from the scan itself.
	count := countHint
	if count == 0 {
		count = tableEntryCount(tx, table)
	}
	fmt.Printf("  entries (stat): %d\n", count)

	shift := uint(64 - bits.TrailingZeros(uint(nShards))) // top log2(nShards) bits route the shard
	keyLen := 20
	if !accounts {
		keyLen = 52
	}

	shardOuts := make([]*shardOut, nShards)
	for s := 0; s < nShards; s++ {
		shardOuts[s] = newShardOut(outDir, prefix, s)
	}

	var dict *codeDict
	if accounts {
		dict = newCodeDict()
	}

	// --- Phase A: single scan → per-shard .val + keyfile ---
	fmt.Printf("  [phase A] single scan → sharded .val + keyfiles...\n")
	tA := time.Now()
	{
		c, err := tx.Cursor(table)
		if err != nil {
			fatal("cursor: %v", err)
		}
		var n uint64
		rsKey := make([]byte, 0, 52)
		rewriteScratch := make([]byte, 0, 64)
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				fatal("iter: %v", err)
			}
			var value []byte
			if accounts {
				rsKey = append(rsKey[:0], k...)
				value = rewriteAccountValue(v, dict, rewriteScratch[:0])
			} else {
				if len(v) < 32 {
					continue
				}
				rsKey = append(rsKey[:0], k...)
				rsKey = append(rsKey, v[:32]...)
				value = v[32:]
			}
			if len(rsKey) != keyLen {
				fatal("unexpected key length %d (want %d): %x", len(rsKey), keyLen, rsKey)
			}
			h := xxhash.Sum64(rsKey)
			shardOuts[h>>shift].add(rsKey, uint32(h), value)
			n++
			if limitEntries > 0 && n >= limitEntries {
				break
			}
			if n%50_000_000 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				fmt.Printf("    ... %dM / %dM scanned, heapMB=%d\n",
					n/1_000_000, count/1_000_000, m.HeapInuse>>20)
			}
		}
		c.Close()
		count = n // exact
	}
	var totalVal uint64
	for _, sh := range shardOuts {
		sh.finishWriters()
		totalVal += sh.scanOffset
	}
	fmt.Printf("  scanned: %d entries, .val total %s\n", count, humanBytes(totalVal))
	fmt.Printf("  phase A time: %s\n", time.Since(tA).Truncate(time.Second))

	// Persist the codeHash dict (accounts only, first-seen order).
	var codedictSize uint64
	if accounts {
		codedictPath := outDir + "/" + prefix + ".codedict"
		f, err := os.Create(codedictPath)
		if err != nil {
			fatal("create codedict: %v", err)
		}
		bw := bufio.NewWriterSize(f, 8<<20)
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(dict.list)))
		bw.Write(hdr[:])
		for _, h := range dict.list {
			bw.Write(h[:])
		}
		if err := bw.Flush(); err != nil {
			fatal("flush codedict: %v", err)
		}
		f.Close()
		codedictSize = fileSize(codedictPath)
		fmt.Printf("  .codedict: %s (%d unique codeHashes)\n",
			humanBytes(codedictSize), len(dict.list))
		dict = nil
	}

	// --- Phase B: parallel per-shard MPHF + slot-order .val permutation ---
	fmt.Printf("  [phase B] building %d MPHFs + slot-order .val (parallel)...\n", nShards)
	tB := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, nShards)
	for s := 0; s < nShards; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			errs[s] = buildShardIndexB(shardOuts[s], keyLen, outDir)
		}(s)
	}
	wg.Wait()
	for s, err := range errs {
		if err != nil {
			fatal("shard %02d index: %v", s, err)
		}
	}
	for _, sh := range shardOuts {
		os.Remove(sh.keyPath)
		os.Remove(sh.valScanPath)
	}
	var idxSize, efSize uint64
	for _, sh := range shardOuts {
		idxSize += fileSize(sh.idxPath)
		efSize += fileSize(sh.efPath)
	}
	fmt.Printf("  .idx+.ef:     %s (%.2f bits/key)\n",
		humanBytes(idxSize+efSize), float64((idxSize+efSize)*8)/float64(count))
	fmt.Printf("  phase B time: %s\n", time.Since(tB).Truncate(time.Second))

	// --- Phase C: zstd compress each shard ---
	var zstSize uint64
	if !skipZstd {
		fmt.Printf("  [phase C] zstd compressing %d shard .val files...\n", nShards)
		tC := time.Now()
		for _, sh := range shardOuts {
			compressFile(sh.valPath, sh.valPath+".zst")
			zstSize += fileSize(sh.valPath + ".zst")
		}
		fmt.Printf("  .val.zst:     %s (%.1f%% of raw)\n", humanBytes(zstSize),
			float64(zstSize)*100/float64(totalVal))
		fmt.Printf("  phase C time: %s\n", time.Since(tC).Truncate(time.Second))
	}

	// --- Summary ---
	fmt.Printf("\n  SUMMARY for %s:\n", prefix)
	fmt.Printf("    Entries:              %d (%d shards)\n", count, nShards)
	fmt.Printf("    .idx (MPHF+offsets):  %s (%.2f bits/key)\n",
		humanBytes(idxSize), float64(idxSize*8)/float64(count))
	if accounts {
		fmt.Printf("    .codedict:            %s\n", humanBytes(codedictSize))
	}
	fmt.Printf("    .val (uncompressed):  %s\n", humanBytes(totalVal))
	if !skipZstd {
		fmt.Printf("    .val.zst (zstd):      %s (%.1f%% retained)\n",
			humanBytes(zstSize), float64(zstSize)*100/float64(totalVal))
	}
	fmt.Printf("    Total time:           %s\n", time.Since(t0).Truncate(time.Second))
}

// buildShardIndexB builds one shard's NoValues MPHF from its keyfile, then
// rewrites the shard's scan-order temp .val into MPHF-slot order in the final
// .val and writes the slot→offset Elias-Fano .ef. This yields the compact v1
// layout (MPHF + external EF, no per-key record array) but per-shard and
// parallel, from a single MDBX scan. On collision the keyfile is replayed
// (MDBX is never rescanned).
func buildShardIndexB(sh *shardOut, keyLen int, tmpDir string) error {
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   int(sh.count),
		BucketSize: 2000,
		IndexFile:  sh.idxPath,
		TmpDir:     tmpDir,
		LeafSize:   8,
		NoValues:   true, // Lookup returns the perfect-hash slot directly
	}, logger)
	if err != nil {
		return err
	}
	defer rs.Close()

	recLen := keyLen + 8
	feed := func() error {
		f, err := os.Open(sh.keyPath)
		if err != nil {
			return err
		}
		defer f.Close()
		br := bufio.NewReaderSize(f, 8<<20)
		rec := make([]byte, recLen)
		for i := uint64(0); i < sh.count; i++ {
			if _, err := io.ReadFull(br, rec); err != nil {
				return fmt.Errorf("keyfile %s rec %d: %w", sh.keyPath, i, err)
			}
			if err := rs.AddKey(rec[:keyLen], 0); err != nil { // offset unused in NoValues
				return err
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
		ok := false
		for retry := 1; retry <= 10; retry++ {
			rs.ResetNextSalt()
			if err := feed(); err != nil {
				return err
			}
			if err := rs.Build(context.Background()); err == nil {
				fmt.Printf("  *** shard %s: collision retry %d succeeded\n", sh.idxPath, retry)
				ok = true
				break
			} else if !rs.Collision() {
				return err
			}
		}
		if !ok {
			return fmt.Errorf("hash collision persists after 10 retries")
		}
	}

	// Empty shard: valid empty idx already written; leave empty .val/.ef.
	if sh.count == 0 {
		if err := os.WriteFile(sh.valPath, nil, 0644); err != nil {
			return err
		}
		return os.WriteFile(sh.efPath, nil, 0644)
	}

	// slotToScan[slot] = scan-order byte offset of the entry whose key hashes
	// to that MPHF slot. Second keyfile pass fills it via the built MPHF.
	idx, err := recsplit.OpenIndex(sh.idxPath)
	if err != nil {
		return err
	}
	defer idx.Close()
	reader := recsplit.NewIndexReader(idx)
	slotToScan := make([]uint64, sh.count)
	{
		f, err := os.Open(sh.keyPath)
		if err != nil {
			return err
		}
		br := bufio.NewReaderSize(f, 8<<20)
		rec := make([]byte, recLen)
		for i := uint64(0); i < sh.count; i++ {
			if _, err := io.ReadFull(br, rec); err != nil {
				f.Close()
				return fmt.Errorf("keyfile %s slot-pass rec %d: %w", sh.keyPath, i, err)
			}
			slot, ok := reader.Lookup(rec[:keyLen])
			if !ok || slot >= sh.count {
				f.Close()
				return fmt.Errorf("shard %s: slot lookup failed at rec %d", sh.idxPath, i)
			}
			slotToScan[slot] = binary.BigEndian.Uint64(rec[keyLen:])
		}
		f.Close()
	}

	// mmap the scan-order temp .val, then copy each entry in slot order.
	scanF, err := os.Open(sh.valScanPath)
	if err != nil {
		return err
	}
	defer scanF.Close()
	st, err := scanF.Stat()
	if err != nil {
		return err
	}
	scan, mh, err := mmap.Mmap(scanF, int(st.Size()))
	if err != nil {
		return err
	}
	defer mmap.Munmap(scan, mh)

	valF, err := os.Create(sh.valPath)
	if err != nil {
		return err
	}
	valW := bufio.NewWriterSize(valF, 8<<20)
	ef := eliasfano32.NewEliasFano(sh.count, sh.scanOffset)
	var outOff uint64
	for slot := uint64(0); slot < sh.count; slot++ {
		so := slotToScan[slot]
		entryLen := 1 + uint64(scan[so]) // len byte + payload
		if so+entryLen > uint64(len(scan)) {
			valF.Close()
			return fmt.Errorf("shard %s: entry at %d overruns scan val", sh.idxPath, so)
		}
		if _, err := valW.Write(scan[so : so+entryLen]); err != nil {
			valF.Close()
			return err
		}
		ef.AddOffset(outOff)
		outOff += entryLen
	}
	if err := valW.Flush(); err != nil {
		valF.Close()
		return err
	}
	if err := valF.Close(); err != nil {
		return err
	}
	ef.Build()
	efF, err := os.Create(sh.efPath)
	if err != nil {
		return err
	}
	efW := bufio.NewWriterSize(efF, 1<<20)
	if err := ef.Write(efW); err != nil {
		efF.Close()
		return err
	}
	if err := efW.Flush(); err != nil {
		efF.Close()
		return err
	}
	return efF.Close()
}

// tableEntryCount returns the table's row count from MDBX B-tree metadata
// (O(1); ms_entries counts every dup item on DupSort tables). Clamped to
// --limit so progress reporting matches the number of rows exported.
func tableEntryCount(tx kv.Tx, table string) uint64 {
	c, err := tx.Cursor(table)
	if err != nil {
		fatal("cursor count: %v", err)
	}
	defer c.Close()
	count, err := c.Count()
	if err != nil {
		fatal("stat count: %v", err)
	}
	if limitEntries > 0 && count > limitEntries {
		count = limitEntries
	}
	return count
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
	// SpeedBestCompression (size-priority) + full-core concurrency: per-shard
	// files are compressed on all cores, so Best stays fast in wall-clock while
	// giving the smallest .val.zst. The window stays at 8 MiB so the streaming
	// reader's decoder memory is unchanged.
	enc, err := zstd.NewWriter(outBuf,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(8<<20),
		zstd.WithEncoderConcurrency(runtime.NumCPU()),
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
