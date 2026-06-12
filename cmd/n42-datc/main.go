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
	"runtime/debug"
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
	"github.com/n42blockchain/N42/modules/rawdb"
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
	if os.Args[1] == "proof" {
		runProof(os.Args[2:])
		return
	}
	if os.Args[1] == "finalize-leaves" {
		// Crash recovery: turn an interrupted build's leaf/chg spill files into
		// queryable segments without re-running the build.
		ffs := flag.NewFlagSet("finalize-leaves", flag.ExitOnError)
		fout := ffs.String("out", "", "DATC dir containing leafspill/")
		_ = ffs.Parse(os.Args[2:])
		if *fout == "" {
			die("--out required")
		}
		if err := finalizeLeafSegments(*fout); err != nil {
			die("finalize: %v", err)
		}
		fmt.Println("leaf segments finalized")
		return
	}
	if os.Args[1] != "build" {
		die("usage: n42-datc build|verify [flags]")
	}
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	srcMode := fs.String("src", "mainnet", "source: mainnet (acctcs/storcs freezer + headerc gold check) | n42 (erigon-style MDBX changesets, internal root oracle + final-state check)")
	chainDir := fs.String("chain", "", "n42 mode: source chaindata dir (e.g. D:/mainnet-bls-full/chaindata)")
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root verification)")
	out := fs.String("out", "", "output MDBX dir")
	endBlock := fs.Uint64("end", 2_000_000, "end block (exclusive)")
	startBlock := fs.Uint64("start", 0, "start block (resume; state must match)")
	alpha := fs.Float64("alpha", 16, "target changes per node per epoch")
	cbar := fs.Float64("cbar", 20, "assumed average changed keys per block")
	batch := fs.Uint64("batch", 20_000, "blocks per MDBX commit (large batches spill MDBX dirty pages and stall)")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	leafSeg := fs.Bool("leaf-seg", false, "stream leaf history to zstd segment files instead of MDBX (mainnet-scale builds; ~10x smaller)")
	gogc := fs.Int("gogc", 400, "GOGC percent (GC was ~25% CPU at the default 100; the live heap is stable so a high target is safe)")
	window := fs.Bool("window", true, "mainnet: batch the root per E_1 window (bpp Path C) instead of per block — identical records, gold check per window")
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
	fwdMode := *srcMode == "n42"
	var acctTbl, storTbl *freezer.FreezerTable
	var hdrs *ethel.HeaderCompactReader
	var srcDB kv.RoDB
	if fwdMode {
		if *chainDir == "" {
			die("--src n42 requires --chain")
		}
		srcDB = openN42Chain(*chainDir, *mapGB)
		defer srcDB.Close()
	} else {
		acctTbl = openCS(*csDir, "acctcs")
		defer acctTbl.Close()
		storTbl = openCS(*csDir, "storcs")
		defer storTbl.Close()
		var err error
		hdrs, err = ethel.OpenHeaderCompact(*hdrDir)
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
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	// DirtySpace: the kv default is 128 MB — a heavy window dirties 2-3 GB of
	// Hashed*/node pages, so every window was SPILLING dirty pages to disk
	// ~20x over (the ~33µs/put mystery across all earlier runs). 16 GB keeps
	// a full batch's dirty set in RAM; commit then writes it once.
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB)*datasize.GB).
		DirtySpace(uint64(16*datasize.GB)).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, t := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcMeta, tFwdAcctCS, tFwdStorCS, tDatcRoots} {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
	if err != nil {
		die("open out mdbx: %v", err)
	}
	defer db.Close()

	debug.SetGCPercent(*gogc)
	debug.SetMemoryLimit(100 << 30) // hard ceiling well under the 128 GB box

	sched := newSchedule(*alpha, *cbar)
	fmt.Printf("DATC build: blocks [%d, %d) α=%.0f C̄=%.0f GOGC=%d\n  epochs/depth: ", *startBlock, *endBlock, *alpha, *cbar, *gogc)
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
		stoLastFull:   newLastFullCache(8 << 20), // ~8M entries ≈ 1 GB heap, read-back beyond

		chgStoAgg:     make(map[string]*[]chgEvent, 1<<14),
		outDir:        *out,
	}
	for d := 0; d <= maxChgDepth; d++ {
		size := 1
		for i := 0; i < d; i++ {
			size *= 16
		}
		b.accDirty[d] = make([]uint16, size)
		b.chgAccAgg[d] = make([]chgSlot, size)
		b.stoDirty[d] = make(map[string]*uint16, 1<<10)
	}
	b.resumed = *startBlock > 0
	b.stoLastFull.resumed = b.resumed
	b.fwdMode = fwdMode
	b.windowing = !fwdMode && *window
	b.winA = make(map[types.Address]*account.StateAccount, 64)
	b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
	if *leafSeg {
		sw, err := newLeafSpillWriter(*out)
		if err != nil {
			die("leaf spill: %v", err)
		}
		b.spill = sw
	}

	if fwdMode {
		srcTx, err := srcDB.BeginRo(context.Background())
		if err != nil {
			die("begin src: %v", err)
		}
		// End block: the chain's current head + 1 (build covers [0, head]).
		if headPtr := rawdb.ReadCurrentBlockNumber(srcTx); headPtr != nil {
			if *endBlock == 0 || *endBlock > *headPtr+1 {
				*endBlock = *headPtr + 1
			}
		}
		// Phase 1 (idempotent): derive forward changesets if not present yet.
		need := true
		if otx, e := db.BeginRo(context.Background()); e == nil {
			if c, e2 := otx.Cursor(tFwdAcctCS); e2 == nil {
				if k, _, _ := c.First(); k != nil {
					need = false
				}
				c.Close()
			}
			otx.Rollback()
		}
		if need {
			fmt.Printf("[convert] deriving forward changesets from %s …\n", *chainDir)
			if err := convertN42Changesets(srcTx, db, *endBlock); err != nil {
				die("convert: %v", err)
			}
		} else {
			fmt.Printf("[convert] forward changesets already present, skipping\n")
		}

		if err := b.run(*startBlock, *endBlock, *batch); err != nil {
			die("%v", err)
		}
		// External gate: final state equality vs source PlainState — only
		// meaningful for a FULL build (PlainState is the head state).
		headPtr := rawdb.ReadCurrentBlockNumber(srcTx)
		if headPtr != nil && *endBlock == *headPtr+1 {
			btx, err := db.BeginRo(context.Background())
			if err != nil {
				die("begin build ro: %v", err)
			}
			defer btx.Rollback()
			if err := finalStateCheck(btx, srcTx, b); err != nil {
				die("FINAL STATE CHECK FAILED: %v", err)
			}
		} else if headPtr != nil {
			fmt.Printf("[final-check] skipped (partial build %d ≤ head %d)\n", *endBlock, *headPtr)
		}
		srcTx.Rollback()
		return
	}

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
	// Bucketing by level is load-bearing: level d's epoch flush iterates ONLY
	// its own bucket — a shared map would make the d0 boundary (every block)
	// scan every deeper level's accumulating entries, going quadratic.
	//
	// ACCOUNT paths are perfectly dense-indexable (level d = the first d
	// nibbles as a base-16 integer, 16^d ≤ 1.05M slots), so they live in flat
	// arrays + a touched list — recordChange runs with ZERO allocations and
	// ZERO hashing there (the string-keyed maps were ~17% CPU once windowing
	// removed the MDBX wall; same cure as QMDB's flatIndex).
	// STORAGE paths carry a sparse 40-byte domain, so they stay maps — but
	// with POINTER values: lookups are alloc-free (m[string(b)] read pattern),
	// mutation goes through the pointer, and only a first insert allocates.
	accDirty   [maxChgDepth + 1][]uint16 // flat: idx = first d nibbles
	accTouched [maxChgDepth + 1][]uint32
	stoDirty   [maxChgDepth + 1]map[string]*uint16

	// Per-path last-record bookkeeping for the diff superblock rule: a FULL
	// node record is written when no prior record exists (or it was a
	// tombstone) or every F-th epoch, bounding the reader's walk-back to F.
	// Lost on restart → next records degrade to FULL (safe, just larger).
	//
	// Account paths are bounded by construction (Σ16^d, d≤5 ≈ 1.12M) and stay
	// a plain map. STORAGE paths grow with distinct (contract, path) — ~300M
	// at mainnet-25M scale — so they live in a capped cache that reads the
	// authoritative answer back from the already-written DatcStorNode records
	// on a miss (lastFullCache below).
	accLastFull map[string]nodeRecState
	stoLastFull *lastFullCache

	// Aggregated change-event buffers for the CURRENT batch. One row per
	// (path, epoch, batch segment) instead of one per event — the MDBX row
	// overhead dominated the per-event format (~60B effective for a ~3B
	// payload). Account side: flat slots (same dense index as accDirty) with
	// the epoch stored per slot — an epoch rollover drains the slot's events
	// inline. Storage side: pointer-valued map keyed d|domain|path|epoch4.
	chgAccAgg        [maxChgDepth + 1][]chgSlot
	chgAccAggTouched [maxChgDepth + 1][]uint32
	chgStoAgg        map[string]*[]chgEvent

	// Sorted-batch write buffers: DATC puts are collected per MDBX batch,
	// sorted by key, and applied sequentially — near-append B-tree insertion
	// instead of random-key thrash (the cgocall 42% of the profile). Flushed
	// on threshold so heavy eras can't balloon a batch's memory.
	chgAccBuf, chgStoBuf, leafABuf, leafSBuf, nodeAccBuf, nodeStoBuf []kvPair

	// keccak caches: hot addresses (miners every block, hot contracts) and hot
	// slots repeat across millions of blocks; hashing them once is ~free.
	addrHashCache map[types.Address][32]byte
	slotHashCache map[types.Hash][32]byte

	chgKeyScratch []byte // reusable storage-side chg key buffer

	resumed bool // resumed builds always write tombstones (cold lastFull maps)

	// fwdMode: n42-chain source — changesets come from the FwdAcctCS/FwdStorCS
	// tables (derived by convertN42Changesets), there is no external header
	// oracle (the chain's header roots are QMDB roots), so each block's
	// computed MPT root is recorded into DatcRoots as the verify oracle.
	fwdMode bool

	// spill, when non-nil (--leaf-seg), receives leaf-history rows instead of
	// the MDBX tables; finalizeLeafSegments turns it into sorted static
	// segments after the build (leafseg.go).
	spill  *leafSpillWriter
	outDir string

	// Window mode (bpp "Path C" adapted): non-boundary blocks only EMIT their
	// DATC records (leaf history, chg events, change bitmaps — all derived
	// from the changesets, no trie access) and accumulate a last-write-wins
	// net; ONE incremental ComputeRoot at each window boundary brings the trie
	// to the boundary block and is gold-checked against header.Root there.
	// The window length is E_1 (the smallest recorded epoch), and every E_d is
	// an exact multiple of it, so the trie is materialized at PRECISELY the
	// epoch-flush boundaries — the resulting node records are byte-identical
	// to per-block construction (applying a window's net equals applying its
	// blocks sequentially). The gold check moves from per-block to per-window
	// (12 blocks on the mainnet schedule); failures name the window.
	windowing bool
	winA      map[types.Address]*account.StateAccount
	winS      map[types.Address]map[types.Hash]*uint256.Int
	lastRoot  types.Hash // root at the last boundary (for empty-window checks)

	leafAPuts, leafSPuts, chgPuts, nodePuts uint64
}

// putLeaf routes one leaf-history row to the segment spill or the MDBX buffer.
func (b *builder) putLeaf(storage bool, k, v []byte) error {
	if b.spill != nil {
		t := leafTableA
		if storage {
			t = leafTableS
		}
		return b.spill.add(t, k, v)
	}
	if storage {
		b.leafSBuf = append(b.leafSBuf, kvPair{k: k, v: v})
	} else {
		b.leafABuf = append(b.leafABuf, kvPair{k: k, v: v})
	}
	return nil
}

type kvPair struct{ k, v []byte }

// nodeRecState tracks the last node record written for a path.
type nodeRecState struct {
	lastFullEpoch uint32
	exists        bool // false = no record yet or last was a tombstone
}

// lastFullCache is a capped nodeRecState map whose misses are answered from
// the already-written node records (floor scan + ≤fullEvery walk-back to the
// last FULL). Entries written during the CURRENT batch are never evicted —
// their rows may still sit uncommitted in the sorted write buffer, where the
// read-back cannot see them.
type lastFullCache struct {
	m       map[string]lfEntry
	cap     int
	seq     uint32
	curN    int    // entries carrying the current seq (pinned, not evictable)
	missRB  uint64 // read-backs performed (stats)
	resumed bool   // resumed build: read-back results are degraded (see lfEntry)
}

type lfEntry struct {
	nodeRecState
	seq uint32
	// degraded marks a read-back result in a RESUMED build: the in-RAM epoch
	// dirty bitmaps were lost at restart, so a DIFF written for this path's
	// first post-restart flush could under-report changed children. The first
	// flush is forced FULL instead (complete state, no bitmap reliance).
	degraded bool
}

func newLastFullCache(capEntries int) *lastFullCache {
	return &lastFullCache{m: make(map[string]lfEntry, 1<<16), cap: capEntries}
}

func (c *lastFullCache) newBatch() { c.seq++; c.curN = 0 }

func (c *lastFullCache) put(pk string, st nodeRecState) {
	c.putEntry(pk, lfEntry{nodeRecState: st, seq: c.seq})
}

func (c *lastFullCache) putEntry(pk string, ne lfEntry) {
	st := ne.nodeRecState
	if e, ok := c.m[pk]; ok {
		if e.seq != c.seq {
			c.curN++
		}
		c.m[pk] = lfEntry{nodeRecState: st, seq: c.seq, degraded: ne.degraded}
		return
	}
	// Evict only when there ARE unpinned entries — a heavy batch can pin more
	// than the whole cap, and scanning an all-pinned map on every insert is a
	// quadratic stall (the 6M A/B froze exactly there). When everything is
	// pinned the map simply grows past cap until newBatch unpins it.
	if evictable := len(c.m) - c.curN; len(c.m) >= c.cap && evictable > 0 {
		drop := c.cap / 8
		if drop > evictable {
			drop = evictable
		}
		for k, e := range c.m {
			if e.seq == c.seq {
				continue
			}
			delete(c.m, k)
			if drop--; drop <= 0 {
				break
			}
		}
	}
	c.m[pk] = lfEntry{nodeRecState: st, seq: c.seq, degraded: ne.degraded}
	c.curN++
}

// get returns the path's last-record state (and whether it is degraded — a
// resumed build's first post-restart flush must be FULL), reading it back
// from the node table on a cache miss. `epoch` is the epoch about to be
// written — the floor scan looks strictly below it.
func (c *lastFullCache) get(tx kv.Tx, table string, pk string, epoch uint64) (nodeRecState, bool, error) {
	if e, ok := c.m[pk]; ok {
		return e.nodeRecState, e.degraded, nil
	}
	c.missRB++
	st, err := readBackLastFull(tx, table, []byte(pk), epoch)
	if err != nil {
		return nodeRecState{}, false, err
	}
	degraded := c.resumed && st.exists
	c.putEntry(pk, lfEntry{nodeRecState: st, seq: c.seq, degraded: degraded})
	return st, degraded, nil
}

// readBackLastFull reconstructs nodeRecState from the committed records of
// one path: floor record below `epoch`, then ≤fullEvery steps back to the
// last FULL when the floor is a DIFF.
func readBackLastFull(tx kv.Tx, table string, path []byte, epoch uint64) (nodeRecState, error) {
	prefix := make([]byte, 0, 1+len(path))
	prefix = append(prefix, byte(len(path)))
	prefix = append(prefix, path...)
	cur, err := tx.Cursor(table)
	if err != nil {
		return nodeRecState{}, err
	}
	defer cur.Close()
	seek := make([]byte, 0, len(prefix)+4)
	seek = append(seek, prefix...)
	seek = binary.BigEndian.AppendUint32(seek, uint32(epoch))
	k, v, err := cur.Seek(seek)
	if err != nil {
		return nodeRecState{}, err
	}
	if k == nil {
		k, v, err = cur.Last()
	} else {
		k, v, err = cur.Prev()
	}
	for {
		if err != nil {
			return nodeRecState{}, err
		}
		if k == nil || !bytesEqualPrefix(k, prefix) || len(k) != len(prefix)+4 {
			return nodeRecState{}, nil // no record below epoch: never existed
		}
		if len(v) == 0 {
			return nodeRecState{}, nil // floor is a tombstone
		}
		if v[0] == nodeRecFull {
			return nodeRecState{lastFullEpoch: binary.BigEndian.Uint32(k[len(prefix):]), exists: true}, nil
		}
		// DIFF: keep walking back to its FULL anchor (bounded by fullEvery).
		// `exists` is true (the floor record is live); only lastFullEpoch is
		// still unknown.
		pk, pv, perr := cur.Prev()
		if perr != nil {
			return nodeRecState{}, perr
		}
		if pk == nil || !bytesEqualPrefix(pk, prefix) || len(pk) != len(prefix)+4 || len(pv) == 0 {
			// Chain truncated (shouldn't happen for a valid DB): degrade to
			// "due for a FULL now" — safe, just a larger next record.
			return nodeRecState{lastFullEpoch: 0, exists: true}, nil
		}
		k, v, err = pk, pv, nil
	}
}

// chgEvent is one change event inside an aggregated row.
type chgEvent struct {
	block  uint32
	nibble byte
}

// chgSlot is one account-side aggregation slot (flat-indexed by path).
type chgSlot struct {
	epoch  uint32
	events []chgEvent
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
	// In --leaf-seg mode the chg rows go to the segment spill like the leaf
	// rows do (write-once, never read back by the builder; StorChg was the
	// LARGEST table of the 6M calibration). Node records stay in MDBX — the
	// lastFullCache read-back needs them queryable.
	if b.spill != nil {
		for _, e := range []struct {
			tab int
			buf *[]kvPair
		}{
			{segTabChgA, &b.chgAccBuf}, {segTabChgS, &b.chgStoBuf},
		} {
			for i := range *e.buf {
				if err := b.spill.add(e.tab, (*e.buf)[i].k, (*e.buf)[i].v); err != nil {
					return err
				}
			}
			*e.buf = (*e.buf)[:0]
		}
	}
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

	// Mainnet mode: pre-decode blocks on a worker pool (changeset decode +
	// key keccaks were ~12% of the single-threaded loop).
	var pipe *decodePipeline
	if !b.fwdMode {
		pipe = startDecodePipeline(b, start, end, 3)
		defer pipe.Stop()
	}

	// Window mode (mainnet): one ComputeRoot per W blocks instead of per
	// block. Requires every recorded epoch to be a multiple of W so the trie
	// materializes exactly at epoch-flush boundaries.
	var W uint64
	if b.windowing {
		W = b.sched.e[1]
		for d := 1; d <= maxChgDepth; d++ {
			if b.sched.e[d]%W != 0 {
				return fmt.Errorf("window mode needs e[%d]=%d divisible by W=%d", d, b.sched.e[d], W)
			}
		}
		if batchBlocks < W*4 {
			return fmt.Errorf("--batch %d too small for window mode (W=%d)", batchBlocks, W)
		}
		if start > 0 {
			hdr, err := b.hdrs.ReadHeader(start - 1)
			if err != nil {
				return fmt.Errorf("window mode resume: read header %d: %w", start-1, err)
			}
			b.lastRoot = hdr.Root
		}
		fmt.Printf("window mode: W=%d (root + gold check per window)\n", W)
	}
	firstWindow := start == 0

	// TrieOf* writes go to a RAM overlay and flush once per batch: the
	// incremental computer rewrites the same hot trie nodes every block, and
	// those per-block MDBX puts (plus cursor read traffic) were >60% of CPU.
	overlay := commitment.NewTrieOverlay()

	for lo := start; lo < end; lo += batchBlocks {
		hi := lo + batchBlocks
		if b.windowing {
			// Commits must land on window boundaries: an uncommitted window net
			// would be lost across a restart.
			hi = hi / W * W
			if hi <= lo {
				hi = lo + W
			}
		}
		if hi > end {
			hi = end
		}
		tx, err := b.db.BeginRw(context.Background())
		if err != nil {
			return err
		}
		wtx := commitment.WrapTrieOverlay(tx, overlay)
		trc = commitment.NewTrieRootComputer()
		trc.SetRwTx(wtx)
		// Ascending-key Hashed* leaf writes: with W=1024 windows the boundary
		// root's Phase 1/2 puts ~200K random keys into ~100GB B-trees — 68% of
		// all cgocall at the CryptoKitties peak. Sorted application restores
		// B-tree locality (the same lever bpp ships with).
		trc.SetSortedWrites(true)

		for n := lo; n < hi; n++ {
			var dec *decodedBlock
			if pipe != nil {
				if dec, err = pipe.Next(n); err != nil {
					tx.Rollback()
					return fmt.Errorf("block %d: %w", n, err)
				}
			}
			if b.windowing {
				for a, h := range dec.ahash {
					b.addrHashCache[a] = h
				}
				for s, h := range dec.shash {
					b.slotHashCache[s] = h
				}
				if err := b.accumulateBlock(wtx, n, dec.dirtyA, dec.dirtyS); err != nil {
					tx.Rollback()
					return fmt.Errorf("block %d: %w", n, err)
				}
				if (n+1)%W == 0 || n+1 == end {
					trc.SetIncremental(!firstWindow)
					firstWindow = false
					if err := b.applyWindow(wtx, trc, n); err != nil {
						tx.Rollback()
						return err
					}
				}
			} else {
				// Block 0 (genesis alloc) builds the trie from scratch (legacy
				// full rebuild); later blocks run incrementally against TrieOf*.
				trc.SetIncremental(n > 0)
				if err := b.block(wtx, trc, n, dec); err != nil {
					tx.Rollback()
					return fmt.Errorf("block %d: %w", n, err)
				}
			}
			// Epoch boundary flush per level: after block n, levels whose epoch
			// ends at n persist their changed nodes' current TrieOf* bytes
			// (read through the overlay — the freshest node state lives there).
			// In window mode every epoch end coincides with a window boundary,
			// so the trie is exactly at block n here.
			for d := 0; d <= maxChgDepth; d++ {
				if (n+1)%b.sched.e[d] == 0 {
					if b.windowing && (n+1)%W != 0 {
						continue // d0 (E=1) mid-window: nothing materializes (all elided)
					}
					if err := b.flushEpoch(wtx, d, b.sched.epochOf(d, n)); err != nil {
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
				if err := b.flushEpoch(wtx, d, b.sched.epochOf(d, hi-1)); err != nil {
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
		// then the trie overlay (sorted, deduped — one final write per hot
		// node instead of one per block) into this tx before committing.
		b.flushChgAgg()
		if err := b.flushAllBufs(tx); err != nil {
			tx.Rollback()
			return err
		}
		if err := overlay.FlushTo(tx); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		b.stoLastFull.newBatch() // committed: cache entries become evictable

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		bps := float64(blocksDone) / time.Since(t0).Seconds()
		fmt.Printf("  block %d / %d  %.0f blk/s  heap=%dMB  leafA=%d leafS=%d chg=%d nodes=%d  lfCache=%d(rb=%d)\n",
			hi, end, bps, m.HeapAlloc>>20, b.leafAPuts, b.leafSPuts, b.chgPuts, b.nodePuts,
			len(b.stoLastFull.m), b.stoLastFull.missRB)
	}
	fmt.Printf("DATC build done: %d blocks in %s\n", blocksDone, time.Since(t0).Round(time.Second))
	if b.spill != nil {
		fmt.Printf("[leafseg] finalizing %d + %d spilled leaf rows …\n", b.spill.rows[leafTableA], b.spill.rows[leafTableS])
		tFin := time.Now()
		if err := b.spill.close(); err != nil {
			return fmt.Errorf("leaf spill close: %w", err)
		}
		if err := finalizeLeafSegments(b.outDir); err != nil {
			return fmt.Errorf("leaf finalize: %w", err)
		}
		fmt.Printf("[leafseg] done in %s\n", time.Since(tFin).Round(time.Second))
	}
	return nil
}

// block applies one block's changesets, verifies the root against the real
// header, and records leaf history + change-index entries.
func (b *builder) block(tx kv.RwTx, trc *commitment.TrieRootComputer, n uint64, dec *decodedBlock) error {
	if dec != nil {
		// Pre-decoded by the pipeline: adopt the dirty maps and merge the
		// worker-computed key hashes into the caches the apply path reads.
		for a, h := range dec.ahash {
			b.addrHashCache[a] = h
		}
		for s, h := range dec.shash {
			b.slotHashCache[s] = h
		}
		return b.blockApply(tx, trc, n, dec.dirtyA, dec.dirtyS)
	}
	dirtyA := make(map[types.Address]*account.StateAccount)
	dirtyS := make(map[types.Address]map[types.Hash]*uint256.Int)

	addAcct := func(addr types.Address, newVal []byte) error {
		if len(newVal) == 0 {
			dirtyA[addr] = nil
			return nil
		}
		var acct account.StateAccount
		if err := acct.DecodeForStorage(newVal); err != nil {
			return fmt.Errorf("decode account %x: %w", addr, err)
		}
		dirtyA[addr] = &acct
		return nil
	}
	addSlot := func(addr types.Address, slot types.Hash, newVal []byte) {
		// Account deleted this block: drop its WRITES (intra-block values
		// that don't survive), but KEEP its wipe entries (new = empty) —
		// they are the per-slot tombstones the leaf history needs
		// (collectPreWipeSlots emits one per pre-existing slot).
		if a, ok := dirtyA[addr]; ok && a == nil && len(newVal) != 0 {
			return
		}
		inner, ok := dirtyS[addr]
		if !ok {
			inner = make(map[types.Hash]*uint256.Int, 8)
			dirtyS[addr] = inner
		}
		if len(newVal) == 0 {
			inner[slot] = nil // delete
		} else {
			inner[slot] = new(uint256.Int).SetBytes(newVal)
		}
	}

	if b.fwdMode {
		// Forward changesets derived from the n42 chain's MDBX changesets.
		var k8 [8]byte
		binary.BigEndian.PutUint64(k8[:], n)
		accBlob, err := tx.GetOne(tFwdAcctCS, k8[:])
		if err != nil {
			return err
		}
		if err := decodeFwdBlob(accBlob, 20, func(key, val []byte) error {
			var addr types.Address
			copy(addr[:], key)
			return addAcct(addr, val)
		}); err != nil {
			return fmt.Errorf("fwd acct blob block %d: %w", n, err)
		}
		stoBlob, err := tx.GetOne(tFwdStorCS, k8[:])
		if err != nil {
			return err
		}
		if err := decodeFwdBlob(stoBlob, 52, func(key, val []byte) error {
			var addr types.Address
			var slot types.Hash
			copy(addr[:], key[:20])
			copy(slot[:], key[20:])
			addSlot(addr, slot, val)
			return nil
		}); err != nil {
			return fmt.Errorf("fwd stor blob block %d: %w", n, err)
		}
	} else {
		accBlob, err := b.acctTbl.Retrieve(n)
		if err != nil {
			return fmt.Errorf("acctcs: %w", err)
		}
		stoBlob, err := b.storTbl.Retrieve(n)
		if err != nil {
			return fmt.Errorf("storcs: %w", err)
		}
		if len(accBlob) > 0 {
			entries, err := ethel.DecodeAccountChanges(accBlob)
			if err != nil {
				return fmt.Errorf("decode acctcs: %w", err)
			}
			for _, e := range entries {
				if err := addAcct(e.Address, e.NewValue); err != nil {
					return err
				}
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
				addSlot(addr, slot, e.NewValue)
			}
		}
	}
	return b.blockApply(tx, trc, n, dirtyA, dirtyS)
}

// accumulateBlock is the WINDOW-MODE per-block path: emit the block's DATC
// records (net-aware ghost/wipe handling — the pre-block state is the window
// net over the MDBX window-start state) and fold the changes into the window
// net. No trie access, no root.
func (b *builder) accumulateBlock(tx kv.RwTx, n uint64,
	dirtyA map[types.Address]*account.StateAccount, dirtyS map[types.Address]map[types.Hash]*uint256.Int) error {

	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil
	}

	// Ghost-storage drop (same rule as blockApply, net-aware pre-state).
	for addr := range dirtyS {
		if _, inA := dirtyA[addr]; inA {
			continue
		}
		exists := false
		if a, ok := b.winA[addr]; ok {
			exists = a != nil
		} else {
			ah := b.addrHash(addr)
			if v, e := tx.GetOne(modules.HashedAccounts, ah[:]); e == nil && len(v) > 0 {
				exists = true
			}
		}
		if !exists {
			delete(dirtyS, addr)
		}
	}
	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil
	}

	// SELFDESTRUCT wipe tombstones: pre-block live slots = MDBX (window-start)
	// adjusted by the window net so far.
	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := b.addrHash(addr)
		live := make(map[[32]byte]bool)
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
			var sh [32]byte
			copy(sh[:], v[:32])
			live[sh] = true
		}
		c.Close()
		for slot, v := range b.winS[addr] {
			sh := b.slotHash(slot)
			if v == nil {
				delete(live, sh)
			} else {
				live[sh] = true
			}
		}
		for sh := range live {
			var comp [72]byte
			copy(comp[:40], prefix)
			copy(comp[40:], sh[:])
			if err := b.putLeaf(true, append(comp[:], blk8[:]...), nil); err != nil {
				return err
			}
			b.leafSPuts++
			b.recordChange(true, comp[:40], nibblesOf(comp[40:]), n)
		}
	}

	// Leaf history + chg events + change bitmaps (identical to blockApply).
	if err := b.emitBlock(n, dirtyA, dirtyS, blk8); err != nil {
		return err
	}

	// Fold into the window net (last write wins; changesets are faithful
	// per-block deltas, so deletes are explicit).
	for a, v := range dirtyA {
		if v == nil {
			// SELFDESTRUCT: nil out the account's accumulated window writes so
			// the boundary ComputeRoot deletes them even if this block's
			// changeset missed an explicit per-slot wipe (the trie-side
			// counterpart of the wipe-tombstone enumeration above).
			for s := range b.winS[a] {
				b.winS[a][s] = nil
			}
		}
		b.winA[a] = v
	}
	for a, slots := range dirtyS {
		m := b.winS[a]
		if m == nil {
			m = make(map[types.Hash]*uint256.Int, len(slots))
			b.winS[a] = m
		}
		for s, v := range slots {
			m[s] = v
		}
	}
	return b.maybeEarlyFlush(tx)
}

// applyWindow brings the trie to boundary block n with one incremental
// ComputeRoot over the window net and gold-checks against header[n].Root.
func (b *builder) applyWindow(tx kv.RwTx, trc *commitment.TrieRootComputer, n uint64) error {
	root := b.lastRoot
	if len(b.winA) > 0 || len(b.winS) > 0 {
		r, err := trc.ComputeRoot(b.winA, b.winS)
		if err != nil {
			return fmt.Errorf("window ComputeRoot →%d: %w", n, err)
		}
		root = r
		b.winA = make(map[types.Address]*account.StateAccount, 64)
		b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
	}
	hdr, err := b.hdrs.ReadHeader(n)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if root != hdr.Root {
		return fmt.Errorf("WINDOW ROOT MISMATCH at block %d (window end): computed %x != header %x", n, root, hdr.Root)
	}
	b.lastRoot = root
	return nil
}

// blockApply runs the tx-dependent part of a block: ghost-storage drop, wipe
// enumeration, root gold check, and DATC record emission.
func (b *builder) blockApply(tx kv.RwTx, trc *commitment.TrieRootComputer, n uint64,
	dirtyA map[types.Address]*account.StateAccount, dirtyS map[types.Address]map[types.Hash]*uint256.Int) error {

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
	if b.fwdMode {
		// No external MPT oracle (the chain's header roots are QMDB roots):
		// record the computed root as the verify oracle. The external gate is
		// the end-of-build final-state equality check against PlainState.
		var rk [8]byte
		binary.BigEndian.PutUint64(rk[:], n)
		if err := tx.Put(tDatcRoots, rk[:], root[:]); err != nil {
			return err
		}
	} else {
		hdr, err := b.hdrs.ReadHeader(n)
		if err != nil {
			return fmt.Errorf("read header: %w", err)
		}
		if root != hdr.Root {
			return fmt.Errorf("ROOT MISMATCH: computed %x != header %x", root, hdr.Root)
		}
	}

	// Record leaf history + change-index entries + pending changed paths.
	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)
	for i := range wiped {
		comp := wiped[i].composite
		if err := b.putLeaf(true, append(comp[:], blk8[:]...), nil); err != nil {
			return err
		}
		b.leafSPuts++
		b.recordChange(true, comp[:40], nibblesOf(comp[40:]), n)
	}
	if err := b.emitBlock(n, dirtyA, dirtyS, blk8); err != nil {
		return err
	}
	return b.maybeEarlyFlush(tx)
}

// emitBlock writes one block's leaf-history rows + change events for the
// dirty maps (shared by the per-block and window paths).
func (b *builder) emitBlock(n uint64,
	dirtyA map[types.Address]*account.StateAccount, dirtyS map[types.Address]map[types.Hash]*uint256.Int,
	blk8 [8]byte) error {
	for addr, acct := range dirtyA {
		ah := b.addrHash(addr)
		var val []byte
		if acct != nil {
			val = acct.MarshalV2()
		}
		if err := b.putLeaf(false, append(append([]byte{}, ah[:]...), blk8[:]...), val); err != nil {
			return err
		}
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
			if err := b.putLeaf(true, append(composite, blk8[:]...), val); err != nil {
				return err
			}
			b.leafSPuts++
			b.recordChange(true, domain, nibblesOf(sh[:]), n)
		}
	}
	return nil
}

// recordChange records one dirty key: the changed-children bitmap per ancestor
// path (drives node diff records) and an aggregated change event per level.
//
// d0 change rows are deliberately NOT written: E_0 = 1 means the floor record
// for any query already sits at block N itself (epoch == block), so the d0
// change window is empty by construction and the verifier never reads it.
func (b *builder) recordChange(storage bool, domain []byte, keyNibbles []byte, n uint64) {
	if storage {
		b.recordChangeStorage(domain, keyNibbles, n)
		return
	}
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	// Account trie: dense flat index — idx_d = first d nibbles, built
	// incrementally. Zero allocations, zero hashing.
	idx := uint32(0)
	for d := 0; d <= maxD; d++ {
		bit := uint16(1) << keyNibbles[d]
		if cur := b.accDirty[d][idx]; cur == 0 {
			b.accTouched[d] = append(b.accTouched[d], idx)
			b.accDirty[d][idx] = bit
		} else if cur&bit == 0 {
			b.accDirty[d][idx] = cur | bit
		}
		if d != 0 && b.sched.e[d] != 1 {
			epoch := uint32(b.sched.epochOf(d, n))
			slot := &b.chgAccAgg[d][idx]
			if len(slot.events) > 0 && slot.epoch != epoch {
				// Epoch rolled over inside the batch: drain the closed epoch's
				// row inline.
				b.drainAccSlot(d, idx, slot)
			}
			if len(slot.events) == 0 {
				if slot.events == nil {
					b.chgAccAggTouched[d] = append(b.chgAccAggTouched[d], idx)
				}
				slot.epoch = epoch
				if slot.events == nil {
					slot.events = make([]chgEvent, 0, 8)
				}
			}
			slot.events = append(slot.events, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgPuts++
		}
		idx = idx*16 + uint32(keyNibbles[d])
	}
}

// recordChangeStorage is the sparse-domain (per-contract) variant: maps stay,
// but with pointer values — repeated touches are alloc-free lookups.
func (b *builder) recordChangeStorage(domain []byte, keyNibbles []byte, n uint64) {
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	// One shared key buffer: d(1) | domain | path nibbles | epoch(4); the
	// dirty key is the [1:1+len(domain)+d] slice, the agg key the whole thing.
	kb := b.chgKeyScratch[:0]
	kb = append(kb, 0)
	kb = append(kb, domain...)
	kb = append(kb, keyNibbles[:maxD]...)
	kb = append(kb, 0, 0, 0, 0)
	b.chgKeyScratch = kb
	for d := 0; d <= maxD; d++ {
		bit := uint16(1) << keyNibbles[d]
		pk := kb[1 : 1+len(domain)+d]
		if p, ok := b.stoDirty[d][string(pk)]; ok {
			*p |= bit // alloc-free hot path
		} else {
			v := bit
			b.stoDirty[d][string(pk)] = &v
		}
		if d == 0 || b.sched.e[d] == 1 {
			continue // empty-window levels: change rows are never consulted
		}
		epoch := b.sched.epochOf(d, n)
		kb[0] = byte(d)
		ak := kb[:1+len(domain)+d+4]
		binary.BigEndian.PutUint32(ak[len(ak)-4:], uint32(epoch))
		if p, ok := b.chgStoAgg[string(ak)]; ok {
			*p = append(*p, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
		} else {
			evs := make([]chgEvent, 0, 8)
			evs = append(evs, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgStoAgg[string(ak)] = &evs
		}
		b.chgPuts++
	}
}

// drainAccSlot encodes one closed account-agg slot into the sorted write
// buffer (prefix d|path|epoch4|firstBlock4) and resets the slot.
func (b *builder) drainAccSlot(d int, idx uint32, slot *chgSlot) {
	k := make([]byte, 0, 1+d+4+4)
	k = append(k, byte(d))
	// Reconstruct the d nibbles from the dense index (big-endian base 16).
	for i := d - 1; i >= 0; i-- {
		k = append(k, 0)
	}
	v := idx
	for i := d; i >= 1; i-- {
		k[i] = byte(v & 0xf)
		v >>= 4
	}
	k = binary.BigEndian.AppendUint32(k, slot.epoch)
	k = binary.BigEndian.AppendUint32(k, slot.events[0].block)
	b.chgAccBuf = append(b.chgAccBuf, kvPair{k: k, v: encodeChgRow(slot.events)})
	slot.events = slot.events[:0]
}

// flushChgAgg drains the per-batch aggregated change buffers into the sorted
// write buffers: one row per (prefix, batch segment), keyed by the segment's
// first block so an epoch spanning batches concatenates in block order.
func (b *builder) flushChgAgg() {
	// Account side: drain the flat slots via the touched lists and reset them.
	for d := 1; d <= maxChgDepth; d++ {
		for _, idx := range b.chgAccAggTouched[d] {
			slot := &b.chgAccAgg[d][idx]
			if len(slot.events) > 0 {
				b.drainAccSlot(d, idx, slot)
			}
			slot.events = nil // release; nil re-arms the touched marker
		}
		b.chgAccAggTouched[d] = b.chgAccAggTouched[d][:0]
	}
	// Storage side: pointer-valued map.
	for ak, events := range b.chgStoAgg {
		if len(*events) > 0 {
			k := make([]byte, 0, len(ak)+4)
			k = append(k, ak...)
			k = binary.BigEndian.AppendUint32(k, (*events)[0].block)
			b.chgStoBuf = append(b.chgStoBuf, kvPair{k: k, v: encodeChgRow(*events)})
		}
		delete(b.chgStoAgg, ak)
	}
}

func bytesEqualPrefix(k, p []byte) bool {
	return len(k) >= len(p) && string(k[:len(p)]) == string(p)
}

// flushEpoch persists the epoch-end node bytes for every path changed during
// the closing epoch of level d, reading the CURRENT TrieOf* rows.
func (b *builder) flushEpoch(tx kv.RwTx, d int, epoch uint64) error {
	// Account side: sorted dense indices (numeric order == path order for a
	// fixed level), path reconstructed from the index.
	if len(b.accTouched[d]) > 0 {
		touched := b.accTouched[d]
		sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
		path := make([]byte, d)
		for _, idx := range touched {
			changed := b.accDirty[d][idx]
			b.accDirty[d][idx] = 0
			if changed == 0 || d == 0 {
				// d0 = the account-trie root: no TrieAccount row by convention;
				// the verifier synthesizes it from the depth-1 children.
				continue
			}
			v := idx
			for i := d - 1; i >= 0; i-- {
				path[i] = byte(v & 0xf)
				v >>= 4
			}
			if err := b.flushAccPath(tx, path, changed, epoch); err != nil {
				return err
			}
		}
		b.accTouched[d] = touched[:0]
	}
	return b.flushStoLevel(tx, d, epoch)
}

// flushAccPath emits one account-trie node record (FULL/DIFF/tombstone).
func (b *builder) flushAccPath(tx kv.RwTx, path []byte, changed uint16, epoch uint64) error {
	node, err := tx.GetOne(modules.TrieOfAccounts, path)
	if err != nil {
		return err
	}
	k := make([]byte, 0, 1+len(path)+4)
	k = append(k, byte(len(path)))
	k = append(k, path...)
	k = binary.BigEndian.AppendUint32(k, uint32(epoch))
	st := b.accLastFull[string(path)]
	var v []byte
	switch {
	case len(node) == 0: // tombstone
		if !st.exists && !b.resumed {
			return nil // never had a live record: elide
		}
		b.accLastFull[string(path)] = nodeRecState{exists: false}
	case !st.exists || uint32(epoch) >= st.lastFullEpoch+fullEvery:
		v = append([]byte{nodeRecFull}, node...)
		b.accLastFull[string(path)] = nodeRecState{lastFullEpoch: uint32(epoch), exists: true}
	default:
		v = encodeNodeDiff(node, changed)
		st.exists = true
		b.accLastFull[string(path)] = st
	}
	b.nodeAccBuf = append(b.nodeAccBuf, kvPair{k: k, v: v})
	b.nodePuts++
	return nil
}

// flushStoLevel emits the storage-trie node records for one level's pending
// paths (sorted; pointer-valued map).
func (b *builder) flushStoLevel(tx kv.RwTx, d int, epoch uint64) error {
	pending := b.stoDirty[d]
	{
		if len(pending) == 0 {
			return nil
		}
		srcTable := modules.TrieOfStorage
		nodeTable := tDatcStoNode
		buf := &b.nodeStoBuf
		getLF := func(pk string) (nodeRecState, bool, error) {
			return b.stoLastFull.get(tx, nodeTable, pk, epoch)
		}
		putLF := func(pk string, st nodeRecState) {
			b.stoLastFull.put(pk, st)
		}
		// Sorted iteration: the TrieOf* GetOne reads walk the B-tree in key
		// order instead of map-random order.
		paths := make([]string, 0, len(pending))
		for pk := range pending {
			paths = append(paths, pk)
		}
		sort.Strings(paths)
		for _, pk := range paths {
			changed := *pending[pk]
			path := []byte(pk)
			delete(pending, pk)
			// Full-key read works for the DupSort table: the kv layer auto-
			// converts keys exactly as trie_root_computer's own Put/Delete do.
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

			st, degraded, err := getLF(pk)
			if err != nil {
				return err
			}
			var v []byte
			switch {
			case len(node) == 0: // tombstone
				if !st.exists {
					// Never had a live record — a tombstone adds nothing (the
					// verifier treats absent and tombstone identically; the
					// storage cache reads back the committed truth, so elision
					// stays safe on resumed builds too).
					continue
				}
				putLF(pk, nodeRecState{exists: false})
			case !st.exists || degraded || uint32(epoch) >= st.lastFullEpoch+fullEvery:
				v = append([]byte{nodeRecFull}, node...)
				putLF(pk, nodeRecState{lastFullEpoch: uint32(epoch), exists: true})
			default:
				v = encodeNodeDiff(node, changed)
				st.exists = true
				putLF(pk, st)
			}
			*buf = append(*buf, kvPair{k: k, v: v})
			b.nodePuts++
		}
		return nil
	}
}

