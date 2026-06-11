// n42-datc — prototype of Depth-Adaptive Temporal Checkpointing (DATC) for
// full-history EIP-1186 proofs (design: docs/ethel/eip1186-mpt-proof-storage-research.md §6).
//
// build mode replays the acctcs/storcs changeset freezer from genesis, maintains
// the erigon-layout state trie incrementally (TrieRootComputer), verifies EVERY
// block's root against the real header (headerc freezer) — the gold correctness
// gate — and writes the DATC temporal records:
//
//	DatcAccNode / DatcStorNode : (path, epochIdx) → MarshalTrieNode bytes at
//	                              epoch end (empty = tombstone)
//	DatcAccChg  / DatcStorChg  : (depth, path, epochIdx, block, childNibble)
//	                              → nil — which child changed when (window index)
//	DatcLeafA   / DatcLeafS    : (hashedKey, block) → value (empty = deleted)
//	                              — the leaf history (key-major changesets)
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths.
//
// Usage:
//
//	n42-datc build --changesets D:/N42-eth1177/chain/freezer \
//	  --headers D:/n42-eth1/chain/freezer --out D:/n42-datc \
//	  --end 2000000 --alpha 16 --cbar 20
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// DATC table names (prototype-local; registered via WithTableCfg).
const (
	tDatcAccNode = "DatcAccNode"
	tDatcStoNode = "DatcStorNode"
	tDatcAccChg  = "DatcAccChg"
	tDatcStoChg  = "DatcStorChg"
	tDatcLeafA   = "DatcLeafA"
	tDatcLeafS   = "DatcLeafS"
	tDatcMeta    = "DatcMeta"
)

// maxChgDepth caps the change-index depth. Deeper levels are resolved by the
// verifier's leaf-history fold, so their (huge-epoch) records and 8-level write
// amplification buy nothing — depth 5 covers every record-driven level the
// verifier consults at its default fold depth.
const maxChgDepth = 5

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+f+"\n", a...)
	os.Exit(1)
}

func openCS(dir, name string) *freezer.FreezerTable {
	t, err := freezer.NewFreezerTableReadOnly(dir, name, "c")
	if err != nil {
		die("open %s: %v", name, err)
	}
	t.ForceBatchSize(freezer.BatchSize)
	t.SetCompressed(true)
	return t
}

// epochSchedule holds per-depth epoch lengths.
type epochSchedule struct{ e [maxChgDepth + 1]uint64 }

func newSchedule(alpha, cbar float64) epochSchedule {
	var s epochSchedule
	for d := 0; d <= maxChgDepth; d++ {
		e := alpha * pow16(d) / cbar
		if e < 1 {
			e = 1
		}
		if e > 1<<22 {
			e = 1 << 22
		}
		s.e[d] = uint64(e)
	}
	return s
}

func pow16(d int) float64 {
	v := 1.0
	for i := 0; i < d; i++ {
		v *= 16
	}
	return v
}

func (s epochSchedule) epochOf(d int, block uint64) uint64 { return block / s.e[d] }

// nibblesOf expands bytes to one-nibble-per-byte (erigon keyHex form, no terminator).
func nibblesOf(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = x >> 4
		out[2*i+1] = x & 0x0f
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		die("usage: n42-datc build|verify [flags]")
	}
	if os.Args[1] == "verify" {
		runVerify(os.Args[2:])
		return
	}
	if os.Args[1] == "diag" {
		runDiag(os.Args[2:])
		return
	}
	if os.Args[1] == "folddiff" {
		runFoldDiff(os.Args[2:])
		return
	}
	if os.Args[1] == "stor" {
		runStor(os.Args[2:])
		return
	}
	if os.Args[1] == "segexport" {
		runSegExport(os.Args[2:])
		return
	}
	if os.Args[1] != "build" {
		die("usage: n42-datc build|verify [flags]")
	}
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root verification)")
	out := fs.String("out", "", "output MDBX dir")
	endBlock := fs.Uint64("end", 2_000_000, "end block (exclusive)")
	startBlock := fs.Uint64("start", 0, "start block (resume; state must match)")
	alpha := fs.Float64("alpha", 16, "target changes per node per epoch")
	cbar := fs.Float64("cbar", 20, "assumed average changed keys per block")
	batch := fs.Uint64("batch", 20_000, "blocks per MDBX commit (large batches spill MDBX dirty pages and stall)")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	pprofPort := fs.Int("pprof.port", 0, "serve net/http/pprof on this port (0=off)")
	_ = fs.Parse(os.Args[2:])
	if *out == "" {
		die("--out required")
	}
	if *pprofPort > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", *pprofPort)
		go func() {
			fmt.Fprintf(os.Stderr, "pprof on http://%s/debug/pprof/\n", addr)
			_ = http.ListenAndServe(addr, nil)
		}()
	}

	logger := log.New()
	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()
	storTbl := openCS(*csDir, "storcs")
	defer storTbl.Close()
	hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		die("open headerc: %v", err)
	}
	defer hdrs.Close()

	avail := uint64(acctTbl.Items())
	if *endBlock > avail {
		*endBlock = avail
	}
	if *endBlock > hdrs.MaxBlock() {
		*endBlock = hdrs.MaxBlock()
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, t := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcMeta} {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
	if err != nil {
		die("open out mdbx: %v", err)
	}
	defer db.Close()

	sched := newSchedule(*alpha, *cbar)
	fmt.Printf("DATC build: blocks [%d, %d) α=%.0f C̄=%.0f\n  epochs/depth: ", *startBlock, *endBlock, *alpha, *cbar)
	for d := 0; d <= maxChgDepth; d++ {
		fmt.Printf("d%d=%d ", d, sched.e[d])
	}
	fmt.Println()

	b := &builder{
		sched: sched, db: db, hdrs: hdrs,
		acctTbl: acctTbl, storTbl: storTbl,
		addrHashCache: make(map[types.Address][32]byte, 1<<16),
		slotHashCache: make(map[types.Hash][32]byte, 1<<16),
		accLastFull:   make(map[string]nodeRecState, 1<<16),
		stoLastFull:   make(map[string]nodeRecState, 1<<16),
		chgAccAgg:     make(map[string][]chgEvent, 1<<14),
		chgStoAgg:     make(map[string][]chgEvent, 1<<14),
	}
	for d := 0; d <= maxChgDepth; d++ {
		b.accDirty[d] = make(map[string]uint16, 1<<10)
		b.stoDirty[d] = make(map[string]uint16, 1<<10)
	}
	b.resumed = *startBlock > 0
	if err := b.run(*startBlock, *endBlock, *batch); err != nil {
		die("%v", err)
	}
}

// builder drives the per-block replay + DATC record writing.
type builder struct {
	sched   epochSchedule
	db      kv.RwDB
	hdrs    *ethel.HeaderCompactReader
	acctTbl *freezer.FreezerTable
	storTbl *freezer.FreezerTable

	// Per-LEVEL pending changed paths since each level's last epoch flush, with
	// the CHANGED-CHILDREN bitmap per path (drives node diff records).
	// Key = nibble path (account trie) / domainKey40+nibble path (storage).
	// Bucketing by level is load-bearing: level d's epoch flush iterates ONLY
	// its own bucket — a shared map would make the d0 boundary (every block)
	// scan every deeper level's accumulating entries, going quadratic.
	accDirty [maxChgDepth + 1]map[string]uint16
	stoDirty [maxChgDepth + 1]map[string]uint16

	// Per-path last-record bookkeeping for the diff superblock rule: a FULL
	// node record is written when no prior record exists (or it was a
	// tombstone) or every F-th epoch, bounding the reader's walk-back to F.
	// Lost on restart → next records degrade to FULL (safe, just larger).
	accLastFull map[string]nodeRecState
	stoLastFull map[string]nodeRecState

	// Aggregated change-event buffers for the CURRENT batch, keyed by the chg
	// row prefix (d|domain|path|epoch4). One row per (path, epoch, batch
	// segment) instead of one per event — the MDBX row overhead dominated the
	// per-event format (~60B effective for a ~3B payload).
	chgAccAgg map[string][]chgEvent
	chgStoAgg map[string][]chgEvent

	// Sorted-batch write buffers: DATC puts are collected per MDBX batch,
	// sorted by key, and applied sequentially — near-append B-tree insertion
	// instead of random-key thrash (the cgocall 42% of the profile). Flushed
	// on threshold so heavy eras can't balloon a batch's memory.
	chgAccBuf, chgStoBuf, leafABuf, leafSBuf, nodeAccBuf, nodeStoBuf []kvPair

	// keccak caches: hot addresses (miners every block, hot contracts) and hot
	// slots repeat across millions of blocks; hashing them once is ~free.
	addrHashCache map[types.Address][32]byte
	slotHashCache map[types.Hash][32]byte

	resumed bool // resumed builds always write tombstones (cold lastFull maps)

	leafAPuts, leafSPuts, chgPuts, nodePuts uint64
}

type kvPair struct{ k, v []byte }

// nodeRecState tracks the last node record written for a path.
type nodeRecState struct {
	lastFullEpoch uint32
	exists        bool // false = no record yet or last was a tombstone
}

// chgEvent is one change event inside an aggregated row.
type chgEvent struct {
	block  uint32
	nibble byte
}

const (
	bufFlushThreshold = 4_000_000 // entries per buffer before an early sorted flush

	// Node record value flags (empty value = tombstone, as before).
	nodeRecFull = 0x01 // flags | MarshalTrieNode bytes
	nodeRecDiff = 0x00 // flags | newMasks(6) | changedMask(2) | 32B × (changed ∧ newHasHash)

	// fullEvery bounds the reader's diff walk-back: at least every F-th epoch
	// (per path) the record is FULL.
	fullEvery = 8
)

// encodeNodeDiff builds a DIFF record from the current node bytes and the
// epoch's changed-children bitmap. Only changed children's hashes are stored;
// the reader fills unchanged ones from the previous record chain.
func encodeNodeDiff(node []byte, changed uint16) []byte {
	hasState, hasTree, hasHash, hashes, _ := trie.UnmarshalTrieNode(node)
	out := make([]byte, 0, 9+32*4)
	out = append(out, nodeRecDiff)
	out = binary.BigEndian.AppendUint16(out, hasState)
	out = binary.BigEndian.AppendUint16(out, hasTree)
	out = binary.BigEndian.AppendUint16(out, hasHash)
	out = binary.BigEndian.AppendUint16(out, changed)
	hi := 0
	for nib := 0; nib < 16; nib++ {
		bit := uint16(1) << nib
		if hasHash&bit == 0 {
			continue
		}
		if changed&bit != 0 {
			out = append(out, hashes[hi*32:hi*32+32]...)
		}
		hi++
	}
	return out
}

// encodeChgRow packs an event list (ascending block) into one aggregated row:
// per event varint(Δblock from previous event) + nibble.
func encodeChgRow(events []chgEvent) []byte {
	out := make([]byte, 0, len(events)*3)
	prev := uint32(0)
	for i, e := range events {
		d := e.block - prev
		if i == 0 {
			d = e.block // first event: absolute-within-epoch via block number
		}
		out = binary.AppendUvarint(out, uint64(d))
		out = append(out, e.nibble)
		prev = e.block
	}
	return out
}

func (b *builder) addrHash(addr types.Address) [32]byte {
	if h, ok := b.addrHashCache[addr]; ok {
		return h
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(addr[:]))
	if len(b.addrHashCache) > 2_000_000 {
		b.addrHashCache = make(map[types.Address][32]byte, 1<<16)
	}
	b.addrHashCache[addr] = h
	return h
}

func (b *builder) slotHash(slot types.Hash) [32]byte {
	if h, ok := b.slotHashCache[slot]; ok {
		return h
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(slot[:]))
	if len(b.slotHashCache) > 2_000_000 {
		b.slotHashCache = make(map[types.Hash][32]byte, 1<<16)
	}
	b.slotHashCache[slot] = h
	return h
}

// flushBuf sorts a buffer by key and applies it to the table sequentially.
func flushBuf(tx kv.RwTx, table string, buf *[]kvPair) error {
	if len(*buf) == 0 {
		return nil
	}
	sort.Slice(*buf, func(i, j int) bool { return string((*buf)[i].k) < string((*buf)[j].k) })
	for i := range *buf {
		if err := tx.Put(table, (*buf)[i].k, (*buf)[i].v); err != nil {
			return err
		}
	}
	*buf = (*buf)[:0]
	return nil
}

// flushAllBufs drains every sorted-batch buffer into the tx.
func (b *builder) flushAllBufs(tx kv.RwTx) error {
	for _, e := range []struct {
		table string
		buf   *[]kvPair
	}{
		{tDatcAccChg, &b.chgAccBuf}, {tDatcStoChg, &b.chgStoBuf},
		{tDatcLeafA, &b.leafABuf}, {tDatcLeafS, &b.leafSBuf},
		{tDatcAccNode, &b.nodeAccBuf}, {tDatcStoNode, &b.nodeStoBuf},
	} {
		if err := flushBuf(tx, e.table, e.buf); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) maybeEarlyFlush(tx kv.RwTx) error {
	if len(b.chgAccBuf) > bufFlushThreshold || len(b.chgStoBuf) > bufFlushThreshold ||
		len(b.leafABuf) > bufFlushThreshold || len(b.leafSBuf) > bufFlushThreshold {
		return b.flushAllBufs(tx)
	}
	return nil
}

func (b *builder) run(start, end, batchBlocks uint64) error {
	t0 := time.Now()
	var trc *commitment.TrieRootComputer
	var blocksDone uint64
	lastBeat := time.Now()
	lastBeatBlocks := uint64(0)

	for lo := start; lo < end; lo += batchBlocks {
		hi := lo + batchBlocks
		if hi > end {
			hi = end
		}
		tx, err := b.db.BeginRw(context.Background())
		if err != nil {
			return err
		}
		trc = commitment.NewTrieRootComputer()
		trc.SetRwTx(tx)

		for n := lo; n < hi; n++ {
			// Block 0 (genesis alloc) builds the trie from scratch (legacy full
			// rebuild); every later block runs incrementally against TrieOf*.
			trc.SetIncremental(n > 0)
			if err := b.block(tx, trc, n); err != nil {
				tx.Rollback()
				return fmt.Errorf("block %d: %w", n, err)
			}
			// Epoch boundary flush per level: after block n, levels whose epoch
			// ends at n persist their changed nodes' current TrieOf* bytes.
			for d := 0; d <= maxChgDepth; d++ {
				if (n+1)%b.sched.e[d] == 0 {
					if err := b.flushEpoch(tx, d, b.sched.epochOf(d, n)); err != nil {
						tx.Rollback()
						return fmt.Errorf("epoch flush d=%d block %d: %w", d, n, err)
					}
				}
			}
			blocksDone++
			// Live heartbeat to STDERR (unbuffered through pipes): block height,
			// instantaneous rate, ETA. Every 10s or 20K blocks, whichever first.
			if blocksDone-lastBeatBlocks >= 20_000 || time.Since(lastBeat) > 10*time.Second {
				inst := float64(blocksDone-lastBeatBlocks) / time.Since(lastBeat).Seconds()
				var eta time.Duration
				if inst > 0 {
					eta = time.Duration(float64(end-n) / inst * float64(time.Second))
				}
				fmt.Fprintf(os.Stderr, "[datc] block %d / %d  %.0f blk/s  ETA %s\n",
					n, end, inst, eta.Round(time.Minute))
				lastBeat, lastBeatBlocks = time.Now(), blocksDone
			}
		}
		if hi == end {
			// Final flush of all partial epochs + meta.
			for d := 0; d <= maxChgDepth; d++ {
				if err := b.flushEpoch(tx, d, b.sched.epochOf(d, hi-1)); err != nil {
					tx.Rollback()
					return err
				}
			}
			meta := make([]byte, 8+8+8)
			binary.BigEndian.PutUint64(meta[0:], hi)
			binary.BigEndian.PutUint64(meta[8:], uint64(b.sched.e[0]))
			binary.BigEndian.PutUint64(meta[16:], uint64(maxChgDepth))
			if err := tx.Put(tDatcMeta, []byte("head"), meta); err != nil {
				tx.Rollback()
				return err
			}
			var sb []byte
			for d := 0; d <= maxChgDepth; d++ {
				sb = binary.BigEndian.AppendUint64(sb, b.sched.e[d])
			}
			if err := tx.Put(tDatcMeta, []byte("sched"), sb); err != nil {
				tx.Rollback()
				return err
			}
		}
		// Drain the aggregated change events, then the sorted-batch buffers,
		// into this tx before committing.
		b.flushChgAgg()
		if err := b.flushAllBufs(tx); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		bps := float64(blocksDone) / time.Since(t0).Seconds()
		fmt.Printf("  block %d / %d  %.0f blk/s  heap=%dMB  leafA=%d leafS=%d chg=%d nodes=%d\n",
			hi, end, bps, m.HeapAlloc>>20, b.leafAPuts, b.leafSPuts, b.chgPuts, b.nodePuts)
	}
	fmt.Printf("DATC build done: %d blocks in %s\n", blocksDone, time.Since(t0).Round(time.Second))
	return nil
}

// block applies one block's changesets, verifies the root against the real
// header, and records leaf history + change-index entries.
func (b *builder) block(tx kv.RwTx, trc *commitment.TrieRootComputer, n uint64) error {
	accBlob, err := b.acctTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("acctcs: %w", err)
	}
	stoBlob, err := b.storTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("storcs: %w", err)
	}

	dirtyA := make(map[types.Address]*account.StateAccount)
	dirtyS := make(map[types.Address]map[types.Hash]*uint256.Int)

	if len(accBlob) > 0 {
		entries, err := ethel.DecodeAccountChanges(accBlob)
		if err != nil {
			return fmt.Errorf("decode acctcs: %w", err)
		}
		for _, e := range entries {
			if len(e.NewValue) == 0 {
				dirtyA[e.Address] = nil
				continue
			}
			var acct account.StateAccount
			if err := acct.DecodeForStorage(e.NewValue); err != nil {
				return fmt.Errorf("decode account %x: %w", e.Address, err)
			}
			dirtyA[e.Address] = &acct
		}
	}
	if len(stoBlob) > 0 {
		entries, err := ethel.DecodeStorageChanges(stoBlob)
		if err != nil {
			return fmt.Errorf("decode storcs: %w", err)
		}
		for _, e := range entries {
			var addr types.Address
			var slot types.Hash
			copy(addr[:], e.CompositeKey[:20])
			copy(slot[:], e.CompositeKey[20:])
			// Account deleted this block: drop its WRITES (intra-block values
			// that don't survive), but KEEP its wipe entries (new = empty) —
			// they are the per-slot tombstones the leaf history needs
			// (collectPreWipeSlots emits one per pre-existing slot).
			if a, ok := dirtyA[addr]; ok && a == nil && len(e.NewValue) != 0 {
				continue
			}
			inner, ok := dirtyS[addr]
			if !ok {
				inner = make(map[types.Hash]*uint256.Int, 8)
				dirtyS[addr] = inner
			}
			if len(e.NewValue) == 0 {
				inner[slot] = nil // delete
			} else {
				v := new(uint256.Int).SetBytes(e.NewValue)
				inner[slot] = v
			}
		}
	}

	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil // empty block: trie unchanged, nothing to record
	}

	// Same-block create+destruct ghost storage: a contract deployed AND
	// selfdestructed within one block nets to "absent → absent", so acctcs has
	// NO entry for it — but storcs still carries the intra-block SSTOREs (the
	// wipes-sidecar gap rebuild_state documents). Those slots do not exist at
	// end of block: writing them would plant ghost rows in the leaf history
	// (resurrected by any later fold) and dark-matter HashedStorage rows.
	// Detection: storage-only address whose account is absent in PRE-state —
	// with no acctcs creation it cannot exist at block end either.
	for addr := range dirtyS {
		if _, inA := dirtyA[addr]; inA {
			continue
		}
		ah := b.addrHash(addr)
		if v, e := tx.GetOne(modules.HashedAccounts, ah[:]); e == nil && len(v) == 0 {
			delete(dirtyS, addr)
		}
	}
	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil
	}

	// SELFDESTRUCT: TrieRootComputer wipes the whole storage subtree of a
	// deleted account, but the LEAF HISTORY needs explicit per-slot tombstones
	// or the verifier's fold resurrects pre-destruct slots at later heights.
	// Enumerate the pre-state slots BEFORE ComputeRoot deletes them.
	type wipedSlot struct {
		composite [72]byte
	}
	var wiped []wipedSlot
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := b.addrHash(addr)
		// HashedStorage is DupSort: the cursor yields key=addrHash+inc (40B)
		// with the slotHash in the VALUE (slotHash32 || slotValue). A plain
		// 72-byte-key scan never matches.
		c, cerr := tx.CursorDupSort(modules.HashedStorage)
		if cerr != nil {
			return cerr
		}
		prefix := make([]byte, 40)
		copy(prefix, ah[:])
		for v, e := c.SeekBothRange(prefix, nil); v != nil && e == nil; _, v, e = c.NextDup() {
			if len(v) < 32 {
				continue
			}
			var ws wipedSlot
			copy(ws.composite[:40], prefix)
			copy(ws.composite[40:], v[:32])
			wiped = append(wiped, ws)
		}
		c.Close()
	}

	root, err := trc.ComputeRoot(dirtyA, dirtyS)
	if err != nil {
		return fmt.Errorf("ComputeRoot: %w", err)
	}
	hdr, err := b.hdrs.ReadHeader(n)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if root != hdr.Root {
		return fmt.Errorf("ROOT MISMATCH: computed %x != header %x", root, hdr.Root)
	}

	// Record leaf history + change-index entries + pending changed paths.
	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)
	for i := range wiped {
		comp := wiped[i].composite
		b.leafSBuf = append(b.leafSBuf, kvPair{k: append(comp[:], blk8[:]...)})
		b.leafSPuts++
		b.recordChange(true, comp[:40], nibblesOf(comp[40:]), n)
	}
	for addr, acct := range dirtyA {
		ah := b.addrHash(addr)
		var val []byte
		if acct != nil {
			val = acct.MarshalV2()
		}
		b.leafABuf = append(b.leafABuf, kvPair{k: append(append([]byte{}, ah[:]...), blk8[:]...), v: val})
		b.leafAPuts++
		b.recordChange(false, nil, nibblesOf(ah[:]), n)
	}
	for addr, slots := range dirtyS {
		ah := b.addrHash(addr)
		// A storage-only change still changes the account-trie leaf (its
		// storageRoot): mark the account path in the change index even though
		// acctcs has no entry for it. (TrieRootComputer does the same when it
		// adds storage-dirty addresses to the account RetainList.) No DatcLeafA
		// entry is needed — the account VALUE is unchanged; the verifier's fold
		// recomputes the storageRoot from the storage leaf history.
		if _, also := dirtyA[addr]; !also {
			b.recordChange(false, nil, nibblesOf(ah[:]), n)
		}
		domain := make([]byte, 40)
		copy(domain, ah[:])
		// incarnation 0 (matches TrieRootComputer composite keys)
		for slot, v := range slots {
			sh := b.slotHash(slot)
			composite := make([]byte, 0, 72+8)
			composite = append(composite, domain...)
			composite = append(composite, sh[:]...)
			var val []byte
			if v != nil && !v.IsZero() {
				bb := v.Bytes32()
				s := 0
				for s < 31 && bb[s] == 0 {
					s++
				}
				val = append([]byte{}, bb[s:]...)
			}
			b.leafSBuf = append(b.leafSBuf, kvPair{k: append(composite, blk8[:]...), v: val})
			b.leafSPuts++
			b.recordChange(true, domain, nibblesOf(sh[:]), n)
		}
	}
	return b.maybeEarlyFlush(tx)
}

// recordChange records one dirty key: the changed-children bitmap per ancestor
// path (drives node diff records) and an aggregated change event per level.
//
// d0 change rows are deliberately NOT written: E_0 = 1 means the floor record
// for any query already sits at block N itself (epoch == block), so the d0
// change window is empty by construction and the verifier never reads it.
func (b *builder) recordChange(storage bool, domain []byte, keyNibbles []byte, n uint64) {
	agg, dirty := b.chgAccAgg, &b.accDirty
	if storage {
		agg, dirty = b.chgStoAgg, &b.stoDirty
	}
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	for d := 0; d <= maxD; d++ {
		// pending path + changed-child bit, bucketed by level
		pk := make([]byte, 0, len(domain)+d)
		pk = append(pk, domain...)
		pk = append(pk, keyNibbles[:d]...)
		dirty[d][string(pk)] |= uint16(1) << keyNibbles[d]
		if d == 0 || b.sched.e[d] == 1 {
			continue // empty-window levels: change rows are never consulted
		}
		epoch := b.sched.epochOf(d, n)
		// aggregated row prefix: depth(1) | domain | path(d) | epoch(4)
		ak := make([]byte, 0, 1+len(domain)+d+4)
		ak = append(ak, byte(d))
		ak = append(ak, domain...)
		ak = append(ak, keyNibbles[:d]...)
		ak = binary.BigEndian.AppendUint32(ak, uint32(epoch))
		agg[string(ak)] = append(agg[string(ak)], chgEvent{block: uint32(n), nibble: keyNibbles[d]})
		b.chgPuts++
	}
}

// flushChgAgg drains the per-batch aggregated change buffers into the sorted
// write buffers: one row per (prefix, batch segment), keyed by the segment's
// first block so an epoch spanning batches concatenates in block order.
func (b *builder) flushChgAgg() {
	drain := func(agg map[string][]chgEvent, buf *[]kvPair) {
		for ak, events := range agg {
			if len(events) == 0 {
				continue
			}
			k := make([]byte, 0, len(ak)+4)
			k = append(k, ak...)
			k = binary.BigEndian.AppendUint32(k, events[0].block)
			*buf = append(*buf, kvPair{k: k, v: encodeChgRow(events)})
			delete(agg, ak)
		}
	}
	drain(b.chgAccAgg, &b.chgAccBuf)
	drain(b.chgStoAgg, &b.chgStoBuf)
}

func bytesEqualPrefix(k, p []byte) bool {
	return len(k) >= len(p) && string(k[:len(p)]) == string(p)
}

// flushEpoch persists the epoch-end node bytes for every path changed during
// the closing epoch of level d, reading the CURRENT TrieOf* rows.
func (b *builder) flushEpoch(tx kv.RwTx, d int, epoch uint64) error {
	flushOne := func(storage bool, pending map[string]uint16) error {
		if len(pending) == 0 {
			return nil
		}
		srcTable := modules.TrieOfAccounts
		buf := &b.nodeAccBuf
		lastFull := b.accLastFull
		if storage {
			srcTable = modules.TrieOfStorage
			buf = &b.nodeStoBuf
			lastFull = b.stoLastFull
		}
		// Sorted iteration: the TrieOf* GetOne reads walk the B-tree in key
		// order instead of map-random order.
		paths := make([]string, 0, len(pending))
		for pk := range pending {
			paths = append(paths, pk)
		}
		sort.Strings(paths)
		for _, pk := range paths {
			changed := pending[pk]
			path := []byte(pk)
			delete(pending, pk)
			if !storage && len(path) == 0 {
				// The account-trie root has no TrieAccount row by convention;
				// the verifier synthesizes it from the depth-1 children.
				continue
			}
			// Full-key read works for both tables: the kv layer auto-converts
			// DupSort keys exactly as trie_root_computer's own Put/Delete do.
			node, err := tx.GetOne(srcTable, path)
			if err != nil {
				return err
			}
			// DATC node record: pathLen(1) | domain|path | epoch(4) → record.
			// Empty value = tombstone; else flags byte (FULL | DIFF). A FULL is
			// forced when no prior record exists (or it was a tombstone) and at
			// least every fullEvery-th epoch, bounding the reader's walk-back.
			k := make([]byte, 0, 1+len(path)+4)
			k = append(k, byte(len(path)))
			k = append(k, path...)
			k = binary.BigEndian.AppendUint32(k, uint32(epoch))

			st := lastFull[pk]
			var v []byte
			switch {
			case len(node) == 0: // tombstone
				if !st.exists && !b.resumed {
					// Never had a live record — a tombstone adds nothing (the
					// verifier treats absent and tombstone identically). On a
					// RESUMED build the map is cold, so tombstones are always
					// written (skipping one for a pre-restart live node would
					// resurrect it).
					continue
				}
				lastFull[pk] = nodeRecState{exists: false}
			case !st.exists || uint32(epoch) >= st.lastFullEpoch+fullEvery:
				v = append([]byte{nodeRecFull}, node...)
				lastFull[pk] = nodeRecState{lastFullEpoch: uint32(epoch), exists: true}
			default:
				v = encodeNodeDiff(node, changed)
				st.exists = true
				lastFull[pk] = st
			}
			*buf = append(*buf, kvPair{k: k, v: v})
			b.nodePuts++
		}
		return nil
	}
	if err := flushOne(false, b.accDirty[d]); err != nil {
		return err
	}
	return flushOne(true, b.stoDirty[d])
}

