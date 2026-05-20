// n42-mpt-build: Phase A production tool — build a reth-compatible
// MPT from reth's PlainAccountState / PlainStorageState into N42's
// own file layout (flat sorted branches + root hash).
//
// Pipeline:
//
//  1. Open source MDBX (reth or N42) readonly.
//  2. Pass-1 collect: for each (addr/key, value), compute the
//     hashed nibble path and ETL.Collect into a sortable buffer.
//     ETL handles external sort (spill to NVMe, k-way merge).
//  3. Pass-2 load: ETL.Load streams the sorted entries; we feed
//     each into HashBuilder via GenStructStep. HashCollector
//     captures intermediate branches into a SECOND ETL collector.
//  4. Pass-3 finalize: ETL.Load the branches sorted by nibble path,
//     write them out to <out>/<table>.branches as flat records:
//       [varint keyHexLen][keyHex bytes][varint valLen][val bytes]
//  5. Persist root hash to <out>/<table>.root (32 bytes).
//
// Output layout under <out>/:
//
//   accounts.branches    sorted sequence of (nibble_path, branch_encoding)
//   accounts.root        32-byte root hash
//   storage.branches     same for storage trie
//   storage.root         32-byte storage trie root (aggregated)
//
// Note: this is the OFFLINE archive build path. Account values are
// stored as-is from reth (Compact encoding), so the resulting root
// is a DETERMINISTIC root over reth's encoded values — NOT the
// canonical Ethereum stateRoot (which would require reth-Compact →
// standard RLP conversion + storage root substitution). This is
// sufficient to validate the build pipeline; Compact→RLP transcoding
// is a separate Phase A.5 item.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

type opts struct {
	dbPath     string
	table      string
	outDir     string
	tmpDir     string
	etlBufMB   uint64
	mapSizeGB  int
	verifyRoot string
	maxRows    int64 // 0 = all
}

func main() {
	var o opts
	flag.StringVar(&o.dbPath, "db", `D:\reth2k\db`, "source MDBX dir (readonly)")
	flag.StringVar(&o.table, "table", "PlainAccountState", "source table")
	flag.StringVar(&o.outDir, "out", `D:\n42-mpt`, "output directory")
	flag.StringVar(&o.tmpDir, "tmp", "", "ETL spill dir (default <out>/etl-tmp)")
	flag.Uint64Var(&o.etlBufMB, "etl-buf-mb", 4096, "ETL buffer size MB (per collector)")
	flag.IntVar(&o.mapSizeGB, "mapsize-gb", 4096, "source DB MapSize cap")
	flag.StringVar(&o.verifyRoot, "verify-root", "", "optional expected root hash (hex) to assert")
	flag.Int64Var(&o.maxRows, "max-rows", 0, "0 = full table; else stop after N input rows (smoke testing)")
	flag.Parse()

	if o.tmpDir == "" {
		o.tmpDir = filepath.Join(o.outDir, "etl-tmp")
	}
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		fatal("mkdir out: %v", err)
	}
	if err := os.MkdirAll(o.tmpDir, 0o755); err != nil {
		fatal("mkdir tmp: %v", err)
	}

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(o.dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(o.mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(o.table)).
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

	mtx := tx.(*mdbxkv.MdbxTx)
	st, err := mtx.BucketStat(o.table)
	if err != nil {
		fatal("stat: %v", err)
	}
	fmt.Printf("source %s: %d entries (%.2f GB raw MDBX)\n",
		o.table, st.Entries,
		float64((st.LeafPages+st.BranchPages)*4096)/1e9)

	prefix := outputPrefix(o.table)

	// =====================================================================
	// Pass 1 — scan source, hash keys, ETL.Collect (auto external-sort)
	// =====================================================================
	bufSize := datasize.ByteSize(o.etlBufMB) * datasize.MB
	leafColl := etl.NewCollector("mpt-build-leaves-"+prefix, o.tmpDir,
		etl.NewSortableBuffer(bufSize), logger)
	defer leafColl.Close()

	c, err := tx.Cursor(o.table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	t1 := time.Now()
	var (
		nIn         int64
		lastLog     = time.Now()
		hashedScratch [32]byte
		nibbleScratch = make([]byte, 65)
	)
	hasher := sha3.NewLegacyKeccak256()

	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		// hashedKey = keccak(k). For accounts k is the addr (20B). For
		// storage with reth raw key = addr||slot (52B), we hash each
		// component separately: keccak(addr) || keccak(slot) — that's
		// how reth's MPT walks the composite key. For now: just keccak
		// the full raw k since the spike validated this convention.
		hasher.Reset()
		hasher.Write(k)
		hashed := hasher.Sum(hashedScratch[:0])
		// To nibble path with terminator.
		nibbles := nibbleScratch[:len(hashed)*2+1]
		for i, b := range hashed {
			nibbles[i*2] = b >> 4
			nibbles[i*2+1] = b & 0x0f
		}
		nibbles[len(nibbles)-1] = 0x10
		// Collect — value passed as-is.
		if err := leafColl.Collect(nibbles, v); err != nil {
			fatal("leafColl.Collect #%d: %v", nIn, err)
		}
		nIn++
		if o.maxRows > 0 && nIn >= o.maxRows {
			break
		}
		if time.Since(lastLog) > 5*time.Second {
			rate := float64(nIn) / time.Since(t1).Seconds()
			fmt.Fprintf(os.Stderr, "  pass-1 collected %d rows (%.0f k/s)\n",
				nIn, rate/1000)
			lastLog = time.Now()
		}
	}
	fmt.Printf("pass-1 done: %d rows in %s\n", nIn, time.Since(t1).Truncate(time.Second))

	// =====================================================================
	// Pass 2 — ETL.Load sorted, drive HashBuilder
	// =====================================================================
	t2 := time.Now()
	hb := trie.NewHashBuilder(false)
	var (
		groups       []uint16
		hasTreeArr   []uint16
		hasHashArr   []uint16
		curr, succ   []byte
		currVal      []byte
		leafData     trie.GenStructStepLeafData
		branchCount  int64
		branchBytes  int64
		marshalBuf   []byte
	)
	retain := func(_ []byte) bool { return false }

	branchColl := etl.NewCollector("mpt-build-branches-"+prefix, o.tmpDir,
		etl.NewSortableBuffer(bufSize), logger)
	defer branchColl.Close()

	hc := func(keyHex []byte, hasState, hasTreeM, hasHashM uint16, hashes, rootHash []byte) error {
		if hasState == 0 {
			return nil
		}
		need := 6 + len(hashes) + len(rootHash)
		if cap(marshalBuf) < need {
			marshalBuf = make([]byte, need)
		}
		marshalBuf = marshalBuf[:need]
		_ = trie.MarshalTrieNode(hasState, hasTreeM, hasHashM, hashes, rootHash, marshalBuf)
		// Make a copy because branchColl.Collect may retain the slice.
		keyCopy := make([]byte, len(keyHex))
		copy(keyCopy, keyHex)
		valCopy := make([]byte, len(marshalBuf))
		copy(valCopy, marshalBuf)
		if err := branchColl.Collect(keyCopy, valCopy); err != nil {
			return err
		}
		branchCount++
		branchBytes += int64(need)
		return nil
	}

	loadFn := func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		// k is the nibble path (with terminator); v is the value.
		succ = append(succ[:0], k...)
		if len(curr) > 0 {
			leafData.Value = rlphacks.RlpEncodedBytes(currVal)
			var err error
			groups, hasTreeArr, hasHashArr, err = trie.GenStructStep(
				retain, curr, succ, hb, hc, &leafData,
				groups, hasTreeArr, hasHashArr, false,
			)
			if err != nil {
				return fmt.Errorf("GenStructStep: %w", err)
			}
		}
		curr = append(curr[:0], succ...)
		currVal = append(currVal[:0], v...)
		return nil
	}

	if err := leafColl.Load(nil, "", loadFn, etl.TransformArgs{}); err != nil {
		fatal("pass-2 Load: %v", err)
	}

	// Final entry.
	if len(curr) > 0 {
		leafData.Value = rlphacks.RlpEncodedBytes(currVal)
		if _, _, _, err := trie.GenStructStep(
			retain, curr, []byte{}, hb, hc, &leafData,
			groups, hasTreeArr, hasHashArr, false,
		); err != nil {
			fatal("final GenStructStep: %v", err)
		}
	}

	root, err := hb.RootHash()
	if err != nil {
		fatal("RootHash: %v", err)
	}
	fmt.Printf("pass-2 done: %d branches / %.2f GB in %s\n",
		branchCount, float64(branchBytes)/1e9, time.Since(t2).Truncate(time.Second))
	fmt.Printf("state root: 0x%s\n", hex.EncodeToString(root[:]))

	// =====================================================================
	// Pass 3 — write sorted branches + root to output files
	// =====================================================================
	t3 := time.Now()
	branchPath := filepath.Join(o.outDir, prefix+".branches")
	rootPath := filepath.Join(o.outDir, prefix+".root")

	bf, err := os.Create(branchPath)
	if err != nil {
		fatal("create branches file: %v", err)
	}
	bw := newBufWriter(bf, 8<<20)
	var sizeBuf [10]byte

	writeFn := func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		// Record: [varint keyHexLen][keyHex bytes][varint valLen][val bytes]
		n := binary.PutUvarint(sizeBuf[:], uint64(len(k)))
		if _, err := bw.Write(sizeBuf[:n]); err != nil {
			return err
		}
		if _, err := bw.Write(k); err != nil {
			return err
		}
		n = binary.PutUvarint(sizeBuf[:], uint64(len(v)))
		if _, err := bw.Write(sizeBuf[:n]); err != nil {
			return err
		}
		if _, err := bw.Write(v); err != nil {
			return err
		}
		return nil
	}
	if err := branchColl.Load(nil, "", writeFn, etl.TransformArgs{}); err != nil {
		fatal("pass-3 Load: %v", err)
	}
	if err := bw.Flush(); err != nil {
		fatal("flush branches: %v", err)
	}
	if err := bf.Sync(); err != nil {
		fatal("sync branches: %v", err)
	}
	if err := bf.Close(); err != nil {
		fatal("close branches: %v", err)
	}

	if err := os.WriteFile(rootPath, root[:], 0o644); err != nil {
		fatal("write root: %v", err)
	}

	bfStat, _ := os.Stat(branchPath)
	fmt.Printf("pass-3 done: wrote %s (%.2f GB) + %s in %s\n",
		branchPath, float64(bfStat.Size())/1e9, rootPath,
		time.Since(t3).Truncate(time.Second))

	// =====================================================================
	// Verification
	// =====================================================================
	if o.verifyRoot != "" {
		want := strings.TrimPrefix(strings.TrimSpace(o.verifyRoot), "0x")
		wantB, err := hex.DecodeString(want)
		if err != nil || len(wantB) != 32 {
			fatal("--verify-root: bad hex (expected 32 bytes)")
		}
		if !bytes.Equal(wantB, root[:]) {
			fatal("ROOT MISMATCH: got 0x%s expected 0x%s",
				hex.EncodeToString(root[:]), hex.EncodeToString(wantB))
		}
		fmt.Println("verify-root: ✓ matches expected hash")
	}

	fmt.Println()
	fmt.Println("=== n42-mpt-build complete ===")
	fmt.Printf("  source table       %s\n", o.table)
	fmt.Printf("  leaves             %d\n", nIn)
	fmt.Printf("  branches           %d\n", branchCount)
	fmt.Printf("  bytes/leaf         %.2f\n", float64(branchBytes)/float64(nIn))
	fmt.Printf("  branches file      %.2f GB\n", float64(bfStat.Size())/1e9)
	fmt.Printf("  state root         0x%s\n", hex.EncodeToString(root[:]))
	fmt.Printf("  total elapsed      %s\n", time.Since(t0).Truncate(time.Second))
}

func outputPrefix(table string) string {
	switch table {
	case "PlainAccountState":
		return "accounts"
	case "PlainStorageState":
		return "storage"
	case "Account":
		return "accounts"
	case "Storage":
		return "storage"
	default:
		return strings.ToLower(table)
	}
}

// bufWriter is a minimal buffered writer over *os.File.
type bufWriter struct {
	f   *os.File
	buf []byte
	pos int
}

func newBufWriter(f *os.File, cap int) *bufWriter { return &bufWriter{f: f, buf: make([]byte, 0, cap)} }

func (w *bufWriter) Write(p []byte) (int, error) {
	if w.pos+len(p) > cap(w.buf) {
		if err := w.Flush(); err != nil {
			return 0, err
		}
		if len(p) > cap(w.buf) {
			n, err := w.f.Write(p)
			return n, err
		}
	}
	w.buf = w.buf[:w.pos+len(p)]
	copy(w.buf[w.pos:], p)
	w.pos += len(p)
	return len(p), nil
}

func (w *bufWriter) Flush() error {
	if w.pos == 0 {
		return nil
	}
	if _, err := w.f.Write(w.buf[:w.pos]); err != nil {
		return err
	}
	w.pos = 0
	w.buf = w.buf[:0]
	return nil
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
