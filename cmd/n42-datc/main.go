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
//	DatcStoRoot                : (addrHash, block) → storage root at the end of
//	                              that block (empty = no storage) — written for
//	                              every contract whose storage changed, at the
//	                              cadence the trie materializes (per block, or
//	                              per window in window mode). Gives account
//	                              proofs their storageRoot in O(1) without
//	                              touching the storage leaf history.
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths.
//
// Usage:
//
//	n42-datc build --changesets D:/N42-eth1177/chain/freezer \
//	  --headers D:/n42-eth1/chain/freezer --out D:/n42-datc \
//	  --end 2000000 --alpha 16 --cbar 20 [--acc-depth 4 --sto-depth 2]
//
// Correctness harness: go test ./cmd/n42-datc/ -run TestE2E (synthetic
// changesets, independent reference MPT, every height reconstructed + proofs
// walked; see datc_e2e_test.go).
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// stopRequested is set by the SIGINT/SIGTERM handler. The build loop checks it
// at each batch boundary and exits cleanly (batch committed + spill cut at a
// frame boundary), so Ctrl+C is SAFE and resumable with --start. Never kill -9
// (truncates the in-flight spill frame — the 2026-06-13 data-loss).
var stopRequested atomic.Bool

// DATC table names (prototype-local; registered via WithTableCfg).
const (
	tDatcAccNode = "DatcAccNode"
	tDatcStoNode = "DatcStorNode"
	tDatcAccChg  = "DatcAccChg"
	tDatcStoChg  = "DatcStorChg"
	tDatcLeafA   = "DatcLeafA"
	tDatcLeafS   = "DatcLeafS"
	tDatcStoRoot = "DatcStoRoot" // addrHash(32)|block(4) → storage root (empty = no storage)
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

func main() {
	if len(os.Args) < 2 {
		die("usage: n42-datc build|verify|proof|bench|segexport|diag|finalize-leaves [flags]")
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
	if os.Args[1] == "bench" {
		runBench(os.Args[2:])
		return
	}
	if os.Args[1] == "prep-state" {
		runPrepState(os.Args[2:])
		return
	}
	if os.Args[1] == "merge" {
		runMerge(os.Args[2:])
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
		die("usage: n42-datc build|verify|proof|bench|segexport|diag|finalize-leaves [flags]")
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
	accRootEpoch := fs.Uint64("acc-root-epoch", 0, "record the account-trie root node every N blocks from the loader (1 = per block, ~16 hashes/block; removes the depth-1..3 fan-out from proofs); 0 = synthesize the root from depth-1 records")
	schedStr := fs.String("sched", "", "explicit per-depth epoch lengths e0,e1,...,e5 (overrides --alpha/--cbar); e0 = storage-root level, e1..e3 = account levels 1..3 (e.g. 1024,16384,1024,1,4194304,4194304 = sparse tops + per-block depth-3)")
	batch := fs.Uint64("batch", 20_000, "blocks per MDBX commit (large batches spill MDBX dirty pages and stall)")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	dirtyGB := fs.Int("dirty.gb", 16, "MDBX DirtySpace GB — raise so a dense batch's dirty pages stay in RAM and commit doesn't spill (cures the multi-minute commit stalls in DeFi-dense regions)")
	stoCacheM := fs.Int("stocache.m", 8, "storage lastFull node cache size, in millions of entries — raise to cut late-block read-back (rb) cgo reads; ~150 B/entry (64 ≈ 10 GB)")
	leavesTotal := fs.Uint64("leaves-total", 4_726_265_247+8_599_658_943, "total leaf-change workload (AccountChangeSets+StorageChangeSets rows) — denominator for the leaf-workload progress %")
	leavesBase := fs.Uint64("leaves-base", 0, "leaves already processed before --start (resume baseline). Auto-loaded from DatcMeta/leafprog when present; only needed to seed a resume from a binary that predated leafprog persistence")
	concurrentRoot := fs.Bool("concurrent-root", false, "parallel per-window root: 16 top-nibble shards on per-worker RoTx ⊕ a 4-table StateOverlay; byte-identical to serial (and gold-checked each window). Window/incremental mode only; ~7-8x on the per-window ComputeRoot (the build's dominant cost in the DeFi-dense region)")
	leafSeg := fs.Bool("leaf-seg", false, "stream leaf history to zstd segment files instead of MDBX (mainnet-scale builds; ~10x smaller)")
	gogc := fs.Int("gogc", 400, "GOGC percent (GC was ~25% CPU at the default 100; the live heap is stable so a high target is safe)")
	memGB := fs.Int("mem.gb", 100, "Go soft memory limit in GB (debug.SetMemoryLimit): the GC works harder as the heap nears it instead of letting GOGC balloon the process into an OOM kill on a shared box")
	window := fs.Bool("window", true, "mainnet: batch the root per E_1 window (bpp Path C) instead of per block — identical records, gold check per window")
	accDepth := fs.Int("acc-depth", 4, "account-trie levels 1..N-1 get node records + change rows; the reader folds subtrees from the leaf history at depth N (persisted in DatcMeta)")
	stoDepth := fs.Int("sto-depth", 2, "storage-trie levels 0..N-1 get node records + change rows; the reader folds at depth N (persisted in DatcMeta)")
	pprofPort := fs.Int("pprof.port", 0, "serve net/http/pprof on this port (0=off)")
	decodeWorkers := fs.Int("decode-workers", 3, "changeset decode pipeline workers (mainnet mode)")
	prefetch := fs.Bool("prefetch", false, "pipeline workers pre-touch the Hashed* pages of upcoming blocks (parallel warm-up of a cold state DB; read-only)")
	_ = fs.Parse(os.Args[2:])
	if *out == "" {
		die("--out required")
	}
	// Was --start given explicitly? If not, we auto-resume from saved progress
	// (an explicit --start, even 0, overrides; 0 = deliberate fresh build).
	startSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "start" {
			startSet = true
		}
	})
	// Graceful shutdown: Ctrl+C (SIGINT) / SIGTERM sets stopRequested; the build
	// loop finishes the current batch (commit + spill frame-cut) then exits
	// cleanly, so the run resumes safely with --start. Safe to interrupt — do
	// NOT kill -9.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\n[datc] interrupt received — finishing current batch, then stopping cleanly (resumable). Do NOT kill -9.")
		stopRequested.Store(true)
	}()
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
	db, err := openDatcDB(logger, *out, *mapGB, *dirtyGB)
	if err != nil {
		die("open out mdbx: %v", err)
	}
	defer db.Close()

	// Auto-resume: when --start is omitted, continue from the per-batch progress
	// block saved in DatcMeta/progress — the operator no longer hand-computes the
	// resume point. Guard: never silently rebuild from 0 over an output that
	// already holds data but has no saved progress (e.g. built by an older binary
	// predating progress persistence) — that would re-append the leaf spill and
	// corrupt it. Require one explicit --start in that case.
	if !startSet {
		if otx, e := db.BeginRo(context.Background()); e == nil {
			pv, _ := otx.GetOne(tDatcMeta, []byte("progress"))
			if len(pv) >= 8 {
				*startBlock = binary.BigEndian.Uint64(pv)
				fmt.Printf("[datc] auto-resume from saved progress: --start %d\n", *startBlock)
			} else {
				hasData := false
				if lp, _ := otx.GetOne(tDatcMeta, []byte("leafprog")); len(lp) >= 8 {
					hasData = true
				}
				if !hasData {
					if c, ce := otx.Cursor(tDatcAccNode); ce == nil {
						k, _, _ := c.First()
						hasData = k != nil
						c.Close()
					}
				}
				if hasData {
					die("output %s already has data but no saved 'progress' (built by an older binary).\n"+
						"  Pass --start <last window boundary> explicitly ONCE; subsequent runs auto-resume.", *out)
				}
				// Truly empty output -> fresh build from block 0.
			}
			otx.Rollback()
		}
	}

	debug.SetGCPercent(*gogc)
	debug.SetMemoryLimit(int64(*memGB) << 30)

	sched := newSchedule(*alpha, *cbar)
	if *schedStr != "" {
		s, err := parseSchedule(*schedStr)
		if err != nil {
			die("--sched: %v", err)
		}
		sched = s
	}
	sched.accRoot = *accRootEpoch
	fmt.Printf("DATC build: blocks [%d, %d) α=%.0f C̄=%.0f GOGC=%d\n  epochs/depth: ", *startBlock, *endBlock, *alpha, *cbar, *gogc)
	for d := 0; d <= maxChgDepth; d++ {
		fmt.Printf("d%d=%d ", d, sched.e[d])
	}
	fmt.Println()

	b := &builder{
		sched: sched, db: db,
		acctTbl: acctTbl, storTbl: storTbl,
		addrHashCache: make(map[types.Address][32]byte, 1<<16),
		slotHashCache: make(map[types.Hash][32]byte, 1<<16),
		accLastFull:   make(map[string]nodeRecState, 1<<16),
		stoLastFull:   newLastFullCache(*stoCacheM << 20), // tunable via --stocache.m; read-back on miss

		chgStoAgg: make(map[string]*[]chgEvent, 1<<14),
		outDir:    *out,

		leavesBase:  *leavesBase,
		leavesTotal: *leavesTotal,
		accDepth:    *accDepth,
		stoDepth:    *stoDepth,
	}
	if *accDepth < 1 || *accDepth > maxChgDepth+1 || *stoDepth < 1 || *stoDepth > maxChgDepth+1 {
		die("--acc-depth/--sto-depth must be in [1, %d]", maxChgDepth+1)
	}
	// Prefer the persisted leaf-progress baseline (exact across resumes); fall
	// back to --leaves-base only when it's absent (resume from an older binary).
	if *startBlock > 0 {
		if otx, e := db.BeginRo(context.Background()); e == nil {
			if lp, _ := otx.GetOne(tDatcMeta, []byte("leafprog")); len(lp) >= 8 {
				b.leavesBase = binary.BigEndian.Uint64(lp)
			}
			otx.Rollback()
		}
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
	if hdrs != nil {
		b.rootOracle = func(n uint64) (types.Hash, error) {
			hdr, err := hdrs.ReadHeader(n)
			if err != nil {
				return types.Hash{}, err
			}
			return hdr.Root, nil
		}
	}
	b.concurrentRoot = *concurrentRoot
	b.decodeWorkers = *decodeWorkers
	b.prefetch = *prefetch
	b.resumed = *startBlock > 0
	b.stoLastFull.resumed = b.resumed
	b.fwdMode = fwdMode
	b.windowing = !fwdMode && *window
	b.winA = make(map[types.Address]*account.StateAccount, 64)
	b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
	b.winWiped = make(map[types.Address]bool)
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
		if headPtr := rawdb.ReadCurrentFullBlockNumber(srcTx); headPtr != nil {
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
		headPtr := rawdb.ReadCurrentFullBlockNumber(srcTx)
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
	chgStoPuts, nodeStoPuts                 uint64 // storage-side subsets (stats)

	// Leaf-workload progress: block% is misleading (the DeFi-dense back half
	// carries most leaf changes), so report against the total leaf-change count.
	// leavesBase = leaves processed in blocks before --start (resume baseline);
	// leaves-done-so-far = leavesBase + leafAPuts + leafSPuts.
	leavesBase  uint64
	leavesTotal uint64

	// concurrentRoot: parallel per-window ComputeRoot (--concurrent-root).
	concurrentRoot bool

	// rootOracle returns the expected state root at block n (the real header
	// root on mainnet). nil = no external oracle (fwdMode records its own
	// computed roots into DatcRoots instead). Window mode requires it.
	rootOracle func(n uint64) (types.Hash, error)

	// stoRoots collects the storage roots the trie loader finalises during
	// ComputeRoot (TrieRootComputer.SetStorageRootHook); emitStoRoots turns
	// them into DatcStoRoot rows. Guarded: concurrent-root shards call the
	// hook from 16 goroutines.
	stoRootsMu sync.Mutex
	stoRoots   map[[32]byte][32]byte
	// dense: full per-child slot frames of every branch the loader
	// collected since the path's last flush (TrieRootComputer.
	// SetDenseNodeHook). Lets mixed nodes (leaf/extension children, whose
	// hashes the TrieOf* rows omit) be recorded as complete FULL/DIFF
	// records instead of MIXED markers. Keyed path (accounts) /
	// domain+path (storage).
	denseAcc, denseSto map[string]denseEntry
	stoRootBuf         []kvPair
	// winWiped: window mode — contracts whose pre-state slots were
	// tombstoned by a SELFDESTRUCT inside the current window.
	winWiped map[types.Address]bool

	// Build statistics (format-change accounting; heartbeat/tests only).
	statMixedBytesSaved uint64 // node bytes replaced by 1-byte MIXED markers
	statMixedElided     uint64 // MIXED epochs elided (floor already MIXED)
	statDenseUpgraded   uint64 // mixed TrieOf rows recorded in full from the dense hook

	// buildStart: first block of this output (DatcMeta/start).
	buildStart uint64

	// decode pipeline width and state prefetch (see pipeline.go).
	decodeWorkers int
	prefetch      bool

	// Record depth per trie: account levels 1..accDepth-1 and storage levels
	// 0..stoDepth-1 get node records + change rows; the reader folds from
	// the leaf history at accDepth / stoDepth. Deeper records would never be
	// read (the reader always folds there), so they are not written.
	accDepth, stoDepth int
}

// statLegacyExtraBytes estimates how many more bytes the pre-v2 layout would
// have used for the rows this build wrote: +8 B incarnation on every storage
// leaf/chg/node key and +4 B block suffix on every leaf row.
func (b *builder) statLegacyExtraBytes() uint64 {
	return b.leafSPuts*(8+4) + b.leafAPuts*4 + b.chgStoPuts*8 + b.nodeStoPuts*8
}

// denseEntry is one collected branch: masks + 33-byte slot per present child.
type denseEntry struct {
	hasState, hasTree uint16
	slots             []byte
}

// onDenseNode is the TrieRootComputer dense-node hook (may run on shard
// goroutines; (nil, nil, 0, 0, nil) resets).
func (b *builder) onDenseNode(accWithInc, keyHex []byte, hasState, hasTree uint16, slots []byte) {
	b.stoRootsMu.Lock()
	defer b.stoRootsMu.Unlock()
	if keyHex == nil && hasState == 0 {
		b.denseAcc = make(map[string]denseEntry, len(b.denseAcc))
		b.denseSto = make(map[string]denseEntry, len(b.denseSto))
		return
	}
	if b.denseAcc == nil {
		b.denseAcc = make(map[string]denseEntry, 1<<12)
		b.denseSto = make(map[string]denseEntry, 1<<12)
	}
	e := denseEntry{hasState: hasState, hasTree: hasTree, slots: append([]byte{}, slots...)}
	if accWithInc == nil {
		b.denseAcc[string(keyHex)] = e
		return
	}
	k := make([]byte, 0, len(accWithInc)+len(keyHex))
	k = append(k, accWithInc...)
	k = append(k, keyHex...)
	b.denseSto[string(k)] = e
}

// takeDense returns (and forgets) the collected dense form of a path as a
// synthetic MarshalTrieNode with every present child hashed (hasHash ==
// hasState), or nil when none was collected or a child is inline.
func (b *builder) takeDense(storage bool, key string) []byte {
	b.stoRootsMu.Lock()
	m := b.denseAcc
	if storage {
		m = b.denseSto
	}
	e, ok := m[key]
	if ok {
		delete(m, key)
	}
	b.stoRootsMu.Unlock()
	if !ok {
		return nil
	}
	const stride = 33
	digits := 0
	for i := 0; i < 16; i++ {
		if e.hasState&(1<<i) != 0 {
			digits++
		}
	}
	if len(e.slots) != digits*stride {
		return nil
	}
	hashes := make([]byte, 0, digits*32)
	for i := 0; i < digits; i++ {
		if e.slots[i*stride] != 0xa0 {
			return nil // inline child: not representable as a hash list
		}
		hashes = append(hashes, e.slots[i*stride+1:i*stride+stride]...)
	}
	buf := make([]byte, 6+len(hashes))
	return trie.MarshalTrieNode(e.hasState, e.hasTree, e.hasState, hashes, nil, buf)
}

// onStorageRoot is the TrieRootComputer storage-root hook. (nil, nil) is the
// reset signal sent before a serial recompute of a diverged concurrent window.
func (b *builder) onStorageRoot(addrHash, root []byte) {
	b.stoRootsMu.Lock()
	defer b.stoRootsMu.Unlock()
	if addrHash == nil {
		b.stoRoots = make(map[[32]byte][32]byte, len(b.stoRoots))
		return
	}
	if b.stoRoots == nil {
		b.stoRoots = make(map[[32]byte][32]byte, 1<<10)
	}
	var k, v [32]byte
	copy(k[:], addrHash)
	copy(v[:], root)
	b.stoRoots[k] = v
}

// emitStoRoots writes the DatcStoRoot rows for block n after a ComputeRoot:
// one row per contract whose storage changed (root from the loader hook, or
// empty when the trie is now empty — verified against HashedStorage so a
// silent hook miss can never plant a wrong root) and one empty row per
// deleted account that had storage. The hook map is reset afterwards.
func (b *builder) emitStoRoots(tx kv.Tx, n uint64,
	accs map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int,
	wiped map[types.Address]bool) error {
	var blk4 [blkLen]byte
	binary.BigEndian.PutUint32(blk4[:], uint32(n))
	emit := func(ah [32]byte, root []byte) {
		k := make([]byte, 0, 32+blkLen)
		k = append(k, ah[:]...)
		k = append(k, blk4[:]...)
		b.stoRootBuf = append(b.stoRootBuf, kvPair{k: k, v: root})
	}
	b.stoRootsMu.Lock()
	roots := b.stoRoots
	b.stoRoots = nil
	b.stoRootsMu.Unlock()
	for addr := range stor {
		if a, ok := accs[addr]; ok && a == nil {
			continue // deleted this block/window: handled below
		}
		ah := b.addrHash(addr)
		if root, ok := roots[ah]; ok {
			emit(ah, root[:])
			continue
		}
		// The loader did not walk this contract's storage: it must be empty
		// now (every live slot was deleted). Anything else is a hook gap.
		has, err := hasAnyStorage(tx, ah)
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("block %d: storage root hook missed contract %x (storage non-empty)", n, addr)
		}
		emit(ah, nil)
	}
	for addr, a := range accs {
		if a == nil && wiped[addr] {
			emit(b.addrHash(addr), nil)
		}
	}
	return nil
}

// hasAnyStorage reports whether HashedStorage holds any slot under addrHash.
func hasAnyStorage(tx kv.Tx, ah [32]byte) (bool, error) {
	c, err := tx.Cursor(modules.HashedStorage)
	if err != nil {
		return false, err
	}
	defer c.Close()
	k, _, err := c.Seek(ah[:])
	if err != nil {
		return false, err
	}
	return k != nil && len(k) >= 32 && string(k[:32]) == string(ah[:]), nil
}

// forEachStorageSlot streams the slot hashes currently stored under addrHash
// (HashedStorage keys are addrHash(32)|slotHash(32)).
func forEachStorageSlot(tx kv.Tx, ah [32]byte, fn func(sh [32]byte) error) error {
	c, err := tx.Cursor(modules.HashedStorage)
	if err != nil {
		return err
	}
	defer c.Close()
	for k, _, e := c.Seek(ah[:]); k != nil; k, _, e = c.Next() {
		if e != nil {
			return e
		}
		if len(k) != 64 || string(k[:32]) != string(ah[:]) {
			break
		}
		var sh [32]byte
		copy(sh[:], k[32:])
		if err := fn(sh); err != nil {
			return err
		}
	}
	return nil
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

// human formats large counts compactly for the heartbeat (1.64B, 285K, 13.33B).
func human(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (b *builder) run(start, end, batchBlocks uint64) error {
	t0 := time.Now()
	// The first block this OUTPUT ever built (persisted as DatcMeta/start so
	// merge can check range contiguity); a resume keeps the original.
	b.buildStart = start
	if otx, e := b.db.BeginRo(context.Background()); e == nil {
		if sv, _ := otx.GetOne(tDatcMeta, []byte("start")); len(sv) == 8 {
			b.buildStart = binary.BigEndian.Uint64(sv)
		}
		otx.Rollback()
	}
	var trc *commitment.TrieRootComputer
	var blocksDone uint64
	lastBeat := time.Now()
	lastBeatBlocks := uint64(0)
	lastBeatLeaves := b.leavesBase + b.leafAPuts + b.leafSPuts

	// Mainnet mode: pre-decode blocks on a worker pool (changeset decode +
	// key keccaks were ~12% of the single-threaded loop).
	var pipe *decodePipeline
	if !b.fwdMode {
		w := b.decodeWorkers
		if w < 1 {
			w = 3
		}
		pipe = startDecodePipeline(b, start, end, w)
		defer pipe.Stop()
	}

	// Window mode (mainnet): one ComputeRoot per W blocks instead of per
	// block. Requires every recorded epoch to be a multiple of W so the trie
	// materializes exactly at epoch-flush boundaries.
	var W uint64
	if b.windowing {
		if b.rootOracle == nil {
			return fmt.Errorf("window mode needs a root oracle")
		}
		W = b.sched.e[1]
		for d := 1; d <= maxChgDepth; d++ {
			if b.sched.e[d]%W != 0 {
				return fmt.Errorf("window mode needs e[%d]=%d divisible by W=%d", d, b.sched.e[d], W)
			}
		}
		if b.sched.accRoot > 0 && b.sched.accRoot%W != 0 {
			return fmt.Errorf("window mode needs --acc-root-epoch %d divisible by W=%d", b.sched.accRoot, W)
		}
		// The trie only materializes at window boundaries, so the depth-0
		// (storage root) level can only be recorded there: its epoch IS the
		// window. Recording that in the schedule (persisted in DatcMeta) keeps
		// the reader's floor/window arithmetic exact for storage roots.
		if b.sched.e[0] != W {
			fmt.Printf("window mode: e[0] %d → W=%d\n", b.sched.e[0], W)
			b.sched.e[0] = W
		}
		if batchBlocks < W*4 {
			return fmt.Errorf("--batch %d too small for window mode (W=%d)", batchBlocks, W)
		}
		if start > 0 {
			// A resumed window build MUST restart on a window boundary: batch
			// commits snap to hi/W*W, so a clean prior run's progress is always
			// a multiple of W. A non-aligned start would put lastRoot (=root of
			// start-1) out of sync with the MDBX incremental base (the last
			// committed boundary), and the first window's ComputeRoot would be
			// missing the [boundary,start) deltas — a confusing first-window
			// gold-check MISMATCH instead of a clear error here.
			if start%W != 0 {
				return fmt.Errorf("window mode: --start %d must be a multiple of W=%d (resume on a window boundary; last committed block is logged as '  block N')", start, W)
			}
			r, err := b.rootOracle(start - 1)
			if err != nil {
				return fmt.Errorf("window mode resume: read header %d: %w", start-1, err)
			}
			b.lastRoot = r
		}
		fmt.Printf("window mode: W=%d (root + gold check per window)\n", W)
	}
	firstWindow := start == 0

	// TrieOf* writes go to a RAM overlay and flush once per batch: the
	// incremental computer rewrites the same hot trie nodes every block, and
	// those per-block MDBX puts (plus cursor read traffic) were >60% of CPU.
	// --concurrent-root uses the 4-table StateOverlay (so 16 read-only shard
	// workers can read this batch's uncommitted Hashed*/TrieOf* via per-worker
	// RoTx ⊕ overlay); otherwise the 2-table TrieOverlay (serial, unchanged).
	var trieOv *commitment.TrieOverlay
	var stateOv *commitment.StateOverlay
	if b.concurrentRoot {
		stateOv = commitment.NewStateOverlay()
	} else {
		trieOv = commitment.NewTrieOverlay()
	}

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
		var wtx kv.RwTx
		if b.concurrentRoot {
			wtx = commitment.WrapStateOverlayRW(tx, stateOv)
		} else {
			wtx = commitment.WrapTrieOverlay(tx, trieOv)
		}
		trc = commitment.NewTrieRootComputer()
		trc.SetRwTx(wtx)
		trc.SetStorageRootHook(b.onStorageRoot)
		trc.SetDenseNodeHook(b.onDenseNode)
		if b.concurrentRoot {
			// Per-window root fans into 16 nibble shards, each opening its own
			// RoTx from b.db and reading committed ⊕ stateOv. Byte-identical to
			// serial; gold-checked at every window boundary.
			trc.SetConcurrentRoot(b.db, stateOv, 16)
		}
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
			} else if b.windowing {
				// fwdMode window path: decode the forward blobs here (the
				// mainnet pipeline is absent).
				dirtyA, dirtyS, derr := b.decodeFwdBlock(wtx, n)
				if derr != nil {
					tx.Rollback()
					return fmt.Errorf("block %d: %w", n, derr)
				}
				dec = &decodedBlock{n: n, dirtyA: dirtyA, dirtyS: dirtyS}
			}
			if b.windowing {
				// Same cap as addrHash()/slotHash(): the direct merge was
				// bypassing it and the slot cache grew to 33 GB live (GC mark
				// was 23% CPU at the DeFi plateau).
				if len(b.addrHashCache) > 2_000_000 {
					b.addrHashCache = make(map[types.Address][32]byte, 1<<16)
				}
				if len(b.slotHashCache) > 2_000_000 {
					b.slotHashCache = make(map[types.Hash][32]byte, 1<<16)
				}
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
			if r := b.sched.accRoot; r > 0 && (n+1)%r == 0 {
				if err := b.flushAccRoot(wtx, n/r); err != nil {
					tx.Rollback()
					return fmt.Errorf("root flush block %d: %w", n, err)
				}
			}
			blocksDone++
			// Live heartbeat to STDERR (unbuffered through pipes): block height,
			// instantaneous rate, ETA. Every 10s or 20K blocks, whichever first.
			if blocksDone-lastBeatBlocks >= 20_000 || time.Since(lastBeat) > 10*time.Second {
				dt := time.Since(lastBeat).Seconds()
				inst := float64(blocksDone-lastBeatBlocks) / dt
				var blkEta time.Duration
				if inst > 0 {
					blkEta = time.Duration(float64(end-n) / inst * float64(time.Second))
				}
				// Leaf-workload progress is the honest one: the DeFi-dense back
				// half carries most leaf changes, so block% runs far ahead of the
				// real work. Lead with leaf%, keep block as a reference.
				leavesDone := b.leavesBase + b.leafAPuts + b.leafSPuts
				lfRate := float64(leavesDone-lastBeatLeaves) / dt
				var lfPct float64
				var lfEta time.Duration
				if b.leavesTotal > 0 {
					lfPct = 100 * float64(leavesDone) / float64(b.leavesTotal)
					if lfRate > 0 && leavesDone < b.leavesTotal {
						lfEta = time.Duration(float64(b.leavesTotal-leavesDone) / lfRate * float64(time.Second))
					}
				}
				fmt.Fprintf(os.Stderr,
					"[datc] leaf %4.1f%% %s/%s  %s lf/s  ETA %s | block %4.1f%% %d/%d %.0f blk/s ETA %s\n",
					lfPct, human(leavesDone), human(b.leavesTotal), human(uint64(lfRate)), lfEta.Round(time.Minute),
					100*float64(n)/float64(end), n, end, inst, blkEta.Round(time.Minute))
				lastBeat, lastBeatBlocks, lastBeatLeaves = time.Now(), blocksDone, leavesDone
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
			if r := b.sched.accRoot; r > 0 {
				if err := b.flushAccRoot(wtx, (hi-1)/r); err != nil {
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
			if err := tx.Put(tDatcMeta, []byte("format"), []byte{datcFormat}); err != nil {
				tx.Rollback()
				return err
			}
			// Storage-root history cadence: 1 = a row per changed block
			// (exact floor); W = a row per window (the reader must check
			// the depth-0 change window before trusting the floor).
			cad := uint64(1)
			if b.windowing {
				cad = W
			}
			for k, v := range map[string]uint64{"srcad": cad, "accdepth": uint64(b.accDepth), "stodepth": uint64(b.stoDepth), "accroot": b.sched.accRoot, "start": b.buildStart} {
				if err := tx.Put(tDatcMeta, []byte(k), binary.BigEndian.AppendUint64(nil, v)); err != nil {
					tx.Rollback()
					return err
				}
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
		var flushErr error
		if b.concurrentRoot {
			flushErr = stateOv.FlushTo(tx)
		} else {
			flushErr = trieOv.FlushTo(tx)
		}
		if err := flushErr; err != nil {
			tx.Rollback()
			return err
		}
		// Persist the cumulative leaf-workload progress atomically with the batch
		// so a resume picks up the exact baseline (no need for --leaves-base).
		var lp [8]byte
		binary.BigEndian.PutUint64(lp[:], b.leavesBase+b.leafAPuts+b.leafSPuts)
		if err := tx.Put(tDatcMeta, []byte("leafprog"), lp[:]); err != nil {
			tx.Rollback()
			return err
		}
		// Resume point: hi is the exclusive end of this committed batch = the next
		// block to process = exactly the --start a resume needs. Saved atomically
		// so an omitted --start auto-resumes here.
		var prog [8]byte
		binary.BigEndian.PutUint64(prog[:], hi)
		if err := tx.Put(tDatcMeta, []byte("progress"), prog[:]); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		b.stoLastFull.newBatch() // committed: cache entries become evictable
		// Cut the leaf-seg spill at a frame boundary per committed batch: a
		// later hard kill then truncates only the in-flight batch's frame
		// (finalize skips it cleanly) rather than one giant whole-run frame.
		// See [[feedback-human-time-is-precious]] — the 2026-06-13 data-loss.
		if b.spill != nil {
			if err := b.spill.flushBatch(); err != nil {
				return fmt.Errorf("spill flushBatch: %w", err)
			}
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		bps := float64(blocksDone) / time.Since(t0).Seconds()
		fmt.Printf("  block %d / %d  %.0f blk/s  heap=%dMB  leafA=%d leafS=%d chg=%d nodes=%d  lfCache=%d(rb=%d)\n",
			hi, end, bps, m.HeapAlloc>>20, b.leafAPuts, b.leafSPuts, b.chgPuts, b.nodePuts,
			len(b.stoLastFull.m), b.stoLastFull.missRB)
		// Graceful stop point: the batch is committed and the spill is cut at a
		// frame boundary, so this is the safe place to honor Ctrl+C. Skip
		// finalize (run is incomplete); spill is retained for resume.
		if stopRequested.Load() && hi < end {
			fmt.Printf("\n[datc] graceful stop at block %d (committed; spill cut at frame boundary).\n"+
				"  Resume: re-run the SAME command — --start is auto-loaded from saved progress (%d).\n"+
				"  (Spill retained. Pass --start only to override.)\n", hi, hi)
			return nil
		}
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
		// worker-computed key hashes into the caches the apply path reads
		// (capped like addrHash()/slotHash() — the merge must not bypass it).
		if len(b.addrHashCache) > 2_000_000 {
			b.addrHashCache = make(map[types.Address][32]byte, 1<<16)
		}
		if len(b.slotHashCache) > 2_000_000 {
			b.slotHashCache = make(map[types.Hash][32]byte, 1<<16)
		}
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
		var err error
		dirtyA, dirtyS, err = b.decodeFwdBlock(tx, n)
		if err != nil {
			return err
		}
		return b.blockApply(tx, trc, n, dirtyA, dirtyS)
	}
	{
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

// decodeFwdBlock decodes block n's forward changeset blobs (fwdMode) into the
// dirty maps blockApply/accumulateBlock consume. Same wipe rule as the mainnet
// decoder: a deleted account keeps its slot wipes (tombstones) but drops its
// non-empty writes.
func (b *builder) decodeFwdBlock(tx kv.Tx, n uint64) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int, error) {
	dirtyA := make(map[types.Address]*account.StateAccount)
	dirtyS := make(map[types.Address]map[types.Hash]*uint256.Int)
	var k8 [8]byte
	binary.BigEndian.PutUint64(k8[:], n)
	accBlob, err := tx.GetOne(tFwdAcctCS, k8[:])
	if err != nil {
		return nil, nil, err
	}
	if err := decodeFwdBlob(accBlob, 20, func(key, val []byte) error {
		var addr types.Address
		copy(addr[:], key)
		if len(val) == 0 {
			dirtyA[addr] = nil
			return nil
		}
		var acct account.StateAccount
		if err := acct.DecodeForStorage(val); err != nil {
			return fmt.Errorf("decode account %x: %w", addr, err)
		}
		dirtyA[addr] = &acct
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("fwd acct blob block %d: %w", n, err)
	}
	stoBlob, err := tx.GetOne(tFwdStorCS, k8[:])
	if err != nil {
		return nil, nil, err
	}
	if err := decodeFwdBlob(stoBlob, 52, func(key, val []byte) error {
		var addr types.Address
		var slot types.Hash
		copy(addr[:], key[:20])
		copy(slot[:], key[20:])
		if a, ok := dirtyA[addr]; ok && a == nil && len(val) != 0 {
			return nil
		}
		inner, ok := dirtyS[addr]
		if !ok {
			inner = make(map[types.Hash]*uint256.Int, 8)
			dirtyS[addr] = inner
		}
		if len(val) == 0 {
			inner[slot] = nil
		} else {
			inner[slot] = new(uint256.Int).SetBytes(val)
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("fwd stor blob block %d: %w", n, err)
	}
	return dirtyA, dirtyS, nil
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
	//
	// MEMORY: never materialize the full live-slot set. The earlier code built
	// `live := map[[32]byte]bool` over EVERY slot of the wiped contract — a
	// single big-storage SELFDESTRUCT ballooned it to 7–18 GB (pprof inuse_space
	// 2026-06-13 showed this as ~25 GB / 67% of live heap, the dominant OOM risk
	// against the 100 GB ceiling). Instead stream the live slots straight from
	// the HashedStorage cursor and keep ONLY the window-net delta (bounded by
	// the W-block window's writes) in memory. The emitted tombstone set is
	// byte-identical: (MDBX slots ∪ window-written) \ window-deleted, each once.
	var blk4 [blkLen]byte
	binary.BigEndian.PutUint32(blk4[:], uint32(n))
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := b.addrHash(addr)

		// Window-net delta for this addr (size ≤ this window's writes — small):
		// deleted slots are no longer live; written slots are live and may or
		// may not also be in MDBX (winPend tracks the not-yet-in-MDBX ones).
		var winDel, winPend map[[32]byte]bool
		if wn := b.winS[addr]; len(wn) > 0 {
			winDel = make(map[[32]byte]bool, len(wn))
			winPend = make(map[[32]byte]bool, len(wn))
			for slot, v := range wn {
				sh := b.slotHash(slot)
				if v == nil {
					winDel[sh] = true
				} else {
					winPend[sh] = true
				}
			}
		}

		emit := func(sh [32]byte) error {
			var comp [stoDomainLen + 32]byte
			copy(comp[:stoDomainLen], ah[:])
			copy(comp[stoDomainLen:], sh[:])
			if err := b.putLeaf(true, append(comp[:], blk4[:]...), nil); err != nil {
				return err
			}
			b.leafSPuts++
			b.winWiped[addr] = true
			b.recordChange(true, comp[:stoDomainLen], nibblesOf(comp[stoDomainLen:]), n)
			return nil
		}

		// Stream MDBX live slots — no full-set map. A slot deleted by the
		// window net is no longer live (skip). A slot also written this window
		// is still live: emit here ONCE and drop it from winPend so the tail
		// loop below doesn't double-emit it (a duplicate would append a
		// redundant chgEvent in recordChangeStorage).
		if err := forEachStorageSlot(tx, ah, func(sh [32]byte) error {
			if winDel[sh] {
				return nil
			}
			delete(winPend, sh)
			return emit(sh)
		}); err != nil {
			return err
		}
		// Slots created this window that aren't in MDBX yet are still live
		// pre-block — emit their tombstones too.
		for sh := range winPend {
			if err := emit(sh); err != nil {
				return err
			}
		}
	}

	// Leaf history + chg events + change bitmaps (identical to blockApply).
	//
	// Invariant (why flushBuf's non-stable sort is safe for the leaf table):
	// the wipe enumeration above and emitBlock below can both emit a row at
	// the SAME (domain||slotHash||block) key, but ONLY ever with value nil.
	// The wipe loop runs only for dirtyA[addr]==nil (SELFDESTRUCT), and the
	// pipeline's decodeOne drops every NON-empty storage write for such an
	// address (a==nil && len(NewValue)!=0 -> skipped), so dirtyS[addr] then
	// holds nil values exclusively -> emitBlock emits nil. When dirtyA[addr]
	// is non-nil the wipe loop skips it entirely (no collision). Hence a
	// key collision is always nil-vs-nil and the sort order is immaterial.
	if err := b.emitBlock(n, dirtyA, dirtyS, blk4); err != nil {
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
	// Read the boundary header first so we can arm the concurrent-root gold check:
	// if --concurrent-root's parallel combine diverges from header.Root, the
	// computer dumps a per-nibble diagnostic and transparently recomputes the
	// window with the serial loader (the trusted oracle) instead of crashing. A
	// genuine data/header mismatch (serial also wrong) still surfaces below.
	want, err := b.rootOracle(n)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	root := b.lastRoot
	if len(b.winA) > 0 || len(b.winS) > 0 {
		if b.concurrentRoot {
			trc.SetExpectRoot(want)
		}
		r, err := trc.ComputeRoot(b.winA, b.winS)
		trc.ClearExpectRoot()
		if err != nil {
			return fmt.Errorf("window ComputeRoot →%d: %w", n, err)
		}
		root = r
		if err := b.emitStoRoots(tx, n, b.winA, b.winS, b.winWiped); err != nil {
			return err
		}
		b.winA = make(map[types.Address]*account.StateAccount, 64)
		b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
		b.winWiped = make(map[types.Address]bool)
	}
	if root != want {
		return fmt.Errorf("WINDOW ROOT MISMATCH at block %d (window end): computed %x != header %x", n, root, want)
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
		composite [stoDomainLen + 32]byte
	}
	var wiped []wipedSlot
	wipedAddrs := make(map[types.Address]bool)
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := b.addrHash(addr)
		if err := forEachStorageSlot(tx, ah, func(sh [32]byte) error {
			var ws wipedSlot
			copy(ws.composite[:stoDomainLen], ah[:])
			copy(ws.composite[stoDomainLen:], sh[:])
			wiped = append(wiped, ws)
			wipedAddrs[addr] = true
			return nil
		}); err != nil {
			return err
		}
	}

	// Arm the concurrent-root gold check (same safety net as applyWindow): when
	// the header root is known, a diverging parallel combine recomputes serially
	// instead of crashing. fwdMode has no header oracle, so it stays unarmed.
	var hdrRoot types.Hash
	var hdrKnown bool
	if b.rootOracle != nil {
		r, herr := b.rootOracle(n)
		if herr != nil {
			return fmt.Errorf("read header: %w", herr)
		}
		hdrRoot, hdrKnown = r, true
		if b.concurrentRoot {
			trc.SetExpectRoot(hdrRoot)
		}
	}
	root, err := trc.ComputeRoot(dirtyA, dirtyS)
	trc.ClearExpectRoot()
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
	} else if hdrKnown && root != hdrRoot {
		return fmt.Errorf("ROOT MISMATCH: computed %x != header %x", root, hdrRoot)
	}

	// Storage-root history for this block (needs the post-ComputeRoot state).
	if err := b.emitStoRoots(tx, n, dirtyA, dirtyS, wipedAddrs); err != nil {
		return err
	}

	// Record leaf history + change-index entries + pending changed paths.
	var blk4 [blkLen]byte
	binary.BigEndian.PutUint32(blk4[:], uint32(n))
	for i := range wiped {
		comp := wiped[i].composite
		if err := b.putLeaf(true, append(comp[:], blk4[:]...), nil); err != nil {
			return err
		}
		b.leafSPuts++
		b.recordChange(true, comp[:stoDomainLen], nibblesOf(comp[stoDomainLen:]), n)
	}
	if err := b.emitBlock(n, dirtyA, dirtyS, blk4); err != nil {
		return err
	}
	return b.maybeEarlyFlush(tx)
}
