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
	"bytes"
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
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
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
	tDatcMeta    = "DatcMeta"
	// tDatcStoRoot — dense per-contract storage-root history: addrHash32|block8 →
	// root32 (empty value = storage emptied that block). Written only by
	// per-block (non-window) builds via the AccRootEmitter hook; the querier
	// falls back to nodeHashAt when a row/table is absent (older DBs).
	tDatcStoRoot = "DatcStoRoot"
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
	if os.Args[1] == "fold-bench" {
		runFoldBench(os.Args[2:])
		return
	}
	if os.Args[1] == "node-hist-size" {
		runNodeHistSize(os.Args[2:])
		return
	}
	if os.Args[1] == "chg-at" {
		runChgAt(os.Args[2:])
		return
	}
	if os.Args[1] == "leaf-audit" {
		runLeafAudit(os.Args[2:])
		return
	}
	if os.Args[1] == "spill-heal" {
		runSpillHeal(os.Args[2:])
		return
	}
	if os.Args[1] == "bench-proof" {
		runBenchProof(os.Args[2:])
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
	schedOverride := fs.String("sched", "", "explicit epoch schedule: comma-separated e[0..5] overriding alpha/cbar (M2 dense shallow = 1,1,1,1,4194304,4194304 — per-block records at depths 0-3, no windows, AsOf point reads)")
	batch := fs.Uint64("batch", 20_000, "blocks per MDBX commit (large batches spill MDBX dirty pages and stall)")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	dirtyGB := fs.Int("dirty.gb", 16, "MDBX DirtySpace GB — raise so a dense batch's dirty pages stay in RAM and commit doesn't spill (cures the multi-minute commit stalls in DeFi-dense regions)")
	stoCacheM := fs.Int("stocache.m", 8, "storage lastFull node cache size, in millions of entries — raise to cut late-block read-back (rb) cgo reads; ~150 B/entry (64 ≈ 10 GB)")
	chgCapGB := fs.Float64("chgcap.gb", 1.5, "drain the storage change-aggregation map mid-batch once its estimated heap exceeds this (0=off, only at batch commit). Caps the one unbounded per-batch buffer in DeFi-dense regions; records are identical (drained segments concatenate in block order)")
	leavesTotal := fs.Uint64("leaves-total", 4_726_265_247+8_599_658_943, "total leaf-change workload (AccountChangeSets+StorageChangeSets rows) — denominator for the leaf-workload progress %")
	leavesBase := fs.Uint64("leaves-base", 0, "leaves already processed before --start (resume baseline). Auto-loaded from DatcMeta/leafprog when present; only needed to seed a resume from a binary that predated leafprog persistence")
	leafSeg := fs.Bool("leaf-seg", false, "stream leaf history to zstd segment files instead of MDBX (mainnet-scale builds; ~10x smaller)")
	gogc := fs.Int("gogc", 400, "GOGC percent (GC was ~25% CPU at the default 100; the live heap is stable so a high target is safe)")
	window := fs.Bool("window", true, "mainnet: batch the root per E_1 window (bpp Path C) instead of per block — identical records, gold check per window")
	concurrentRoot := fs.Bool("concurrent-root", false, "parallelize the per-window root across 16 top-nibble shards over a 4-table StateOverlay (each window still gold-checked vs header)")
	stateOverlayF := fs.Bool("state-overlay", false, "SERIAL builds: absorb HashedAccounts/HashedStorage writes in the 4-table RAM StateOverlay too (not just TrieOf*), flushing once per batch — at DeFi-era density the per-block Hashed* MDBX puts are ~38% of CPU")
	pprofPort := fs.Int("pprof.port", 0, "serve net/http/pprof on this port (0=off)")
	bisect := fs.Bool("bisect", false, "READ-ONLY diagnosis: replay [resume-start, --end) per block over an uncommitted tx (NEVER commits, NEVER touches the leaf spill), gold-checking EACH block's incremental root against its header. Halts at and reports the FIRST divergent block. Use to localize a window-mode gold-check mismatch to a single block. Output dir is left untouched.")
	dumpChangeset := fs.Uint64("dump-changeset", 0, "decode ONE block's changeset (the dirtyA/dirtyS fed to the fold) and print it, then exit — for cross-checking N42's per-block state delta against an independent source")
	scanGaps := fs.Bool("scan-gaps", false, "scan [resume-start, --end) for MISSING changesets: blocks whose acctcs+storcs blobs are both empty BUT whose header stateRoot changed from the previous block (so the block DID mutate state, yet no changeset was recorded). Fast: reads blob lengths + headers only, no fold. Reports each gap range.")
	changesetFallback := fs.String("changeset-fallback", "", "secondary erigon-style MDBX (AccountChangeSet/StorageChangeSet, e.g. D:/N42-hashed/chaindata) to SPLICE missing changesets from: for each block in [resume-start,--end) whose primary acctcs/storcs freezer blob is empty, derive the block's forward delta from this datadir and inject it into the fold. Fixes resume-gaps in the primary freezer WITHOUT modifying it. Applies to build, --bisect and --scan-gaps.")
	spliceChangesets := fs.String("splice-changesets", "", "like --changeset-fallback, but PERMANENTLY write the derived gap-block deltas INTO the primary acctcs/storcs freezer (Append-overwrite from the first gap block; non-gap tail blocks are read back and re-appended unchanged). Backs up the affected tail .cdat segments + cidx to <backup-dir> first. Requires --splice-backup. Verify afterwards with --bisect (no fallback).")
	spliceBackup := fs.String("splice-backup", "", "directory to copy the affected acctcs/storcs tail segments + cidx into before --splice-changesets mutates them (required for --splice-changesets)")
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
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		DirtySpace(uint64(*dirtyGB) * uint64(datasize.GB)).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, t := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcMeta, tDatcStoRoot, tFwdAcctCS, tFwdStorCS, tDatcRoots} {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
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
	debug.SetMemoryLimit(100 << 30) // hard ceiling well under the 128 GB box

	sched := newSchedule(*alpha, *cbar)
	if *schedOverride != "" {
		parts := strings.Split(*schedOverride, ",")
		if len(parts) != maxChgDepth+1 {
			die("--sched needs exactly %d comma-separated values", maxChgDepth+1)
		}
		for d, p := range parts {
			var v uint64
			if _, err := fmt.Sscanf(strings.TrimSpace(p), "%d", &v); err != nil || v == 0 {
				die("--sched entry %d (%q) must be a positive integer", d, p)
			}
			sched.e[d] = v
		}
	}
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
		stoLastFull:   newLastFullCache(*stoCacheM << 20), // tunable via --stocache.m; read-back on miss

		chgStoAgg: make(map[string]*[]chgEvent, 1<<14),
		outDir:    *out,

		leavesBase:  *leavesBase,
		leavesTotal: *leavesTotal,
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
	b.resumed = *startBlock > 0
	b.stoLastFull.resumed = b.resumed
	b.fwdMode = fwdMode
	b.windowing = !fwdMode && *window
	b.concurrentRoot = *concurrentRoot
	b.stateOverlayOn = *stateOverlayF
	b.chgAggCapBytes = int(*chgCapGB * float64(datasize.GB))
	// concurrent-root works per-window AND per-block: the shard fan-out runs on
	// whatever RetainList one ComputeRoot carries. Per-block mode arms the
	// header root before each fold so a shard divergence self-heals via the
	// serial fallback (and the AccRootEmitter re-fires there — no double emit).
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

	if *dumpChangeset > 0 {
		b.dumpChangeset(*dumpChangeset)
		return
	}

	if *scanGaps {
		b.scanGaps(*startBlock, *endBlock)
		return
	}

	// Permanently splice missing changesets INTO the primary freezer, then exit.
	if *spliceChangesets != "" {
		if *spliceBackup == "" {
			die("--splice-changesets requires --splice-backup <dir>")
		}
		if err := b.spliceChangesetsToFreezer(*spliceChangesets, *csDir, *spliceBackup, *startBlock, *endBlock); err != nil {
			die("splice-changesets: %v", err)
		}
		return
	}

	// Splice missing changesets from a secondary chain (resume-gap repair) before
	// any build/bisect consumes them.
	if *changesetFallback != "" {
		if _, err := b.loadChangesetFallback(*changesetFallback, *startBlock, *endBlock); err != nil {
			die("changeset-fallback: %v", err)
		}
	}

	if *bisect {
		if err := b.bisectRun(*startBlock, *endBlock); err != nil {
			die("%v", err)
		}
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

	// csFallback: derived forward changesets for resume-gap blocks whose primary
	// freezer blob is empty (see loadChangesetFallback / --changeset-fallback).
	// decodeOne injects these so the fold consumes a complete changeset. nil when
	// the flag is off.
	csFallback map[uint64]*fbBlock

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
	// chgStoAgg is the only per-batch buffer with unbounded live growth (one
	// entry per touched (level,domain,path,epoch); DeFi-dense batches put it at
	// ~3 GB). chgStoAggBytes estimates its heap; when it exceeds chgAggCapBytes
	// (>0), maybeEarlyFlush drains it mid-batch — flushChgAgg already keys each
	// drained segment by its first block, so segments concatenate in block order
	// and an early drain is just an earlier batch boundary (records unchanged).
	chgStoAggBytes int
	chgAggCapBytes int

	// Sorted-batch write buffers: DATC puts are collected per MDBX batch,
	// sorted by key, and applied sequentially — near-append B-tree insertion
	// instead of random-key thrash (the cgocall 42% of the profile). Flushed
	// on threshold so heavy eras can't balloon a batch's memory.
	chgAccBuf, chgStoBuf, leafABuf, leafSBuf, nodeAccBuf, nodeStoBuf []kvPair

	// Dense storage-root history (per-block builds only): the AccRootEmitter
	// hook fills stoRootEmits during ComputeRoot; blockApply then writes one
	// tDatcStoRoot row per storage-dirty contract (absent emit ⇒ tombstone:
	// the contract's storage emptied this block).
	stoRootEmits map[string]types.Hash
	stoRootBuf   []kvPair

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

	// concurrentRoot: --concurrent-root. When set, the per-window root fans the
	// CalcTrieRoot into 16 top-nibble shards (each on its own RoTx over the
	// committed DB ⊕ a 4-table StateOverlay holding this batch's uncommitted
	// writes), instead of the serial 2-table TrieOverlay path. The combined root
	// is byte-identical to serial (proven by trie_root_concurrent_test.go) and
	// every window still gold-checks against the header — a mismatch HALTS the
	// build naming the window, so this is safe to validate on real data.
	concurrentRoot bool
	stateOverlayOn bool

	leafAPuts, leafSPuts, chgPuts, nodePuts uint64

	// Leaf-workload progress: block% is misleading (the DeFi-dense back half
	// carries most leaf changes), so report against the total leaf-change count.
	// leavesBase = leaves processed in blocks before --start (resume baseline);
	// leaves-done-so-far = leavesBase + leafAPuts + leafSPuts.
	leavesBase  uint64
	leavesTotal uint64
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

// bisectRun is a READ-ONLY per-block diagnosis: it replays [start,end) over ONE
// uncommitted RwTx, computing each block's incremental root and gold-checking it
// against the header, then halts at the FIRST block whose root diverges. It
// NEVER commits and forces the leaf spill OFF, so the output dir's committed
// state is left intact (MDBX discards the tx on Rollback). Use it to localize a
// window-mode gold-check mismatch (which only checks window boundaries) down to
// the exact block.
//
// It exercises the PER-BLOCK fold path (blockApply, native reads — no overlay).
// If it reports NO divergence through the failing window, the bug is SPECIFIC to
// the window-net fold (accumulateBlock/applyWindow), not the shared per-block
// fold or the changeset data.
// dumpChangeset decodes ONE block's changeset (the exact dirtyA/dirtyS the fold
// consumes) and prints it sorted, then returns. Read-only; opens nothing on the
// output trie. Used to cross-check N42's per-block state delta at a divergent
// block against an independent execution (e.g. reth): a missing/extra/wrong
// account or slot here is a DATA bug; an identical changeset that still folds to
// the wrong root is a FOLD bug.
func (b *builder) dumpChangeset(n uint64) {
	pipe := startDecodePipeline(b, n, n+1, 1)
	defer pipe.Stop()
	dec, err := pipe.Next(n)
	if err != nil {
		die("decode block %d: %v", n, err)
	}

	accs := make([]types.Address, 0, len(dec.dirtyA))
	for a := range dec.dirtyA {
		accs = append(accs, a)
	}
	sort.Slice(accs, func(i, j int) bool { return bytes.Compare(accs[i][:], accs[j][:]) < 0 })
	fmt.Printf("block %d changeset: %d account changes, %d storage-touched accounts\n", n, len(dec.dirtyA), len(dec.dirtyS))
	if dec.err != nil {
		fmt.Printf("  DECODE ERROR: %v\n", dec.err)
	}
	for _, a := range accs {
		acct := dec.dirtyA[a]
		if acct == nil {
			fmt.Printf("ACCT %x  DELETED (selfdestruct/empty)\n", a)
			continue
		}
		fmt.Printf("ACCT %x  nonce=%d balance=%s root=%x codeHash=%x\n",
			a, acct.Nonce, acct.Balance.String(), acct.Root, acct.CodeHash)
	}
	saccs := make([]types.Address, 0, len(dec.dirtyS))
	for a := range dec.dirtyS {
		saccs = append(saccs, a)
	}
	sort.Slice(saccs, func(i, j int) bool { return bytes.Compare(saccs[i][:], saccs[j][:]) < 0 })
	for _, a := range saccs {
		slots := dec.dirtyS[a]
		keys := make([]types.Hash, 0, len(slots))
		for s := range slots {
			keys = append(keys, s)
		}
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
		for _, s := range keys {
			v := slots[s]
			vs := "0 (CLEAR)"
			if v != nil && !v.IsZero() {
				vs = v.String()
			}
			fmt.Printf("STOR %x  %x = %s\n", a, s, vs)
		}
	}
	releaseDecodedBlock(dec)
}

// scanGaps reports MISSING-changeset blocks in [start,end): a block whose acctcs
// AND storcs blobs are both empty, yet whose header stateRoot differs from the
// previous block's — i.e. the block provably mutated state but no changeset was
// recorded for it. Such blocks are silently skipped by the per-block fold (empty
// changeset => no-op, no gold check), so the state drifts until the next NON-empty
// block's gold check fails. Reports contiguous gaps. Reads blob lengths + headers
// only (no fold); fast.
func (b *builder) scanGaps(start, end uint64) {
	fmt.Printf("[scan-gaps] scanning [%d, %d) for blocks that changed state but have an empty changeset …\n", start, end)
	prevRoot, e := b.hdrs.ReadHeader(start - 1)
	if e != nil {
		die("read header %d: %v", start-1, e)
	}
	prev := prevRoot.Root
	gapStart := uint64(0)
	gaps := 0
	emitGap := func(lo, hi uint64) {
		gaps++
		fmt.Printf("  MISSING changeset: blocks [%d, %d] (%d block(s)) changed state but recorded NO changeset\n", lo, hi, hi-lo+1)
	}
	for n := start; n < end; n++ {
		hdr, err := b.hdrs.ReadHeader(n)
		if err != nil {
			die("read header %d: %v", n, err)
		}
		ab, _ := b.acctTbl.Retrieve(n)
		sb, _ := b.storTbl.Retrieve(n)
		emptyCS := len(ab) == 0 && len(sb) == 0
		changed := hdr.Root != prev
		missing := emptyCS && changed
		if missing {
			if gapStart == 0 {
				gapStart = n
			}
		} else if gapStart != 0 {
			emitGap(gapStart, n-1)
			gapStart = 0
		}
		prev = hdr.Root
	}
	if gapStart != 0 {
		emitGap(gapStart, end-1)
	}
	if gaps == 0 {
		fmt.Printf("[scan-gaps] no missing-changeset blocks in range.\n")
	} else {
		fmt.Printf("[scan-gaps] %d gap range(s) found — the changeset source (D:/N42-eth1177) is missing these blocks' state deltas.\n", gaps)
	}
}

func (b *builder) bisectRun(start, end uint64) error {
	b.spill = nil // never touch the leaf segment files
	fmt.Printf("[bisect] read-only per-block replay [%d, %d) — NEVER commits; output left intact\n", start, end)

	tx, err := b.db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback() // diagnosis only — discard every write

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true) // base = committed trie at start-1
	trc.SetSortedWrites(true)

	pipe := startDecodePipeline(b, start, end, 3)
	defer pipe.Stop()

	t0 := time.Now()
	lastBeat := time.Now()
	for n := start; n < end; n++ {
		dec, derr := pipe.Next(n)
		if derr != nil {
			return fmt.Errorf("block %d decode: %w", n, derr)
		}
		// blockApply resolves addrHash()/slotHash() from these caches.
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
		// blockApply does ghost-storage drop + ComputeRoot + per-block gold check,
		// returning a "ROOT MISMATCH" error at the first divergent block.
		if err := b.blockApply(tx, trc, n, dec.dirtyA, dec.dirtyS); err != nil {
			releaseDecodedBlock(dec)
			fmt.Printf("\n[bisect] FIRST DIVERGENCE at block %d:\n  %v\n", n, err)
			fmt.Printf("[bisect] block %d is the first block whose per-block fold diverges from its header.\n"+
				"  Next: diff this block's changeset (D:/N42-eth1177) fold against the canonical state change.\n", n)
			return nil
		}
		releaseDecodedBlock(dec)
		if time.Since(lastBeat) > 10*time.Second {
			fmt.Fprintf(os.Stderr, "[bisect] %d OK  (%.0f blk/s)\n", n, float64(n-start+1)/time.Since(t0).Seconds())
			lastBeat = time.Now()
		}
	}
	fmt.Printf("\n[bisect] NO per-block divergence in [%d, %d).\n"+
		"  => the window-mode mismatch is SPECIFIC to the window-net fold (accumulateBlock/applyWindow),\n"+
		"     NOT the per-block fold or the changeset data.\n", start, end)
	return nil
}

func (b *builder) run(start, end, batchBlocks uint64) error {
	t0 := time.Now()
	var trc *commitment.TrieRootComputer
	var blocksDone uint64
	lastBeat := time.Now()
	lastBeatBlocks := uint64(0)
	lastBeatLeaves := b.leavesBase + b.leafAPuts + b.leafSPuts

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
	//
	// --concurrent-root uses the 4-table StateOverlay instead (it ALSO absorbs
	// HashedAccounts/HashedStorage, so the 16 read-only shard workers can read
	// committed-DB ⊕ this batch's uncommitted writes via per-worker RoTx). The
	// serial path keeps the lighter 2-table TrieOverlay. Exactly one is non-nil.
	var overlay *commitment.TrieOverlay
	var stateOverlay *commitment.StateOverlay
	if b.concurrentRoot || b.stateOverlayOn {
		// 4-table overlay: also absorbs the per-block Hashed* puts (~38% of
		// CPU in DeFi-dense eras); one sorted, deduped flush per batch.
		stateOverlay = commitment.NewStateOverlay()
	} else {
		overlay = commitment.NewTrieOverlay()
	}
	wrap := func(tx kv.RwTx) kv.RwTx {
		if stateOverlay != nil {
			return commitment.WrapStateOverlayRW(tx, stateOverlay)
		}
		return commitment.WrapTrieOverlay(tx, overlay)
	}
	flushOverlay := func(tx kv.RwTx) error {
		if stateOverlay != nil {
			return stateOverlay.FlushTo(tx)
		}
		return overlay.FlushTo(tx)
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
		wtx := wrap(tx)
		trc = commitment.NewTrieRootComputer()
		trc.SetRwTx(wtx)
		if !b.windowing {
			// Dense storage-root history: per-block roots surface every folded
			// contract's storage root — capture them for tDatcStoRoot. Window
			// builds skip this (a window-end root is not the root at inner
			// blocks; the querier falls back to nodeHashAt).
			trc.SetAccRootEmitter(func(accNib []byte, root types.Hash) {
				if b.stoRootEmits == nil {
					return
				}
				var ah [32]byte
				for i := 0; i < 32; i++ {
					ah[i] = accNib[2*i]<<4 | accNib[2*i+1]
				}
				b.stoRootEmits[string(ah[:])] = root
			})
		}
		if b.concurrentRoot {
			// Fan the per-window CalcTrieRoot across 16 nibble shards reading
			// b.db committed ⊕ stateOverlay (this batch's uncommitted writes).
			trc.SetConcurrentRoot(b.db, stateOverlay, 16)
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
			// dec is fully consumed (winA/winS hold the retained pointers, hash
			// caches hold the key hashes) — recycle its maps for the next block.
			if dec != nil {
				releaseDecodedBlock(dec)
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
			if !b.windowing {
				// Storage-root history completeness stamp: the layer covers
				// [stoRootFrom, head]. Written once (first commit); 0 = complete
				// from genesis ⇒ the querier treats misses as "no storage".
				if ex, _ := tx.GetOne(tDatcMeta, []byte("stoRootFrom")); len(ex) != 8 {
					var sf [8]byte
					binary.BigEndian.PutUint64(sf[:], start)
					if err := tx.Put(tDatcMeta, []byte("stoRootFrom"), sf[:]); err != nil {
						tx.Rollback()
						return err
					}
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
		if err := flushOverlay(tx); err != nil {
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
	//
	// MEMORY: never materialize the full live-slot set. The earlier code built
	// `live := map[[32]byte]bool` over EVERY slot of the wiped contract — a
	// single big-storage SELFDESTRUCT ballooned it to 7–18 GB (pprof inuse_space
	// 2026-06-13 showed this as ~25 GB / 67% of live heap, the dominant OOM risk
	// against the 100 GB ceiling). Instead stream the live slots straight from
	// the HashedStorage cursor and keep ONLY the window-net delta (bounded by
	// the W-block window's writes) in memory. The emitted tombstone set is
	// byte-identical: (MDBX slots ∪ window-written) \ window-deleted, each once.
	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)
	for addr, acct := range dirtyA {
		if acct != nil {
			continue
		}
		ah := b.addrHash(addr)
		prefix := make([]byte, 40)
		copy(prefix, ah[:])

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
			var comp [72]byte
			copy(comp[:40], prefix)
			copy(comp[40:], sh[:])
			if err := b.putLeaf(true, append(comp[:], blk8[:]...), nil); err != nil {
				return err
			}
			b.leafSPuts++
			b.recordChange(true, comp[:40], nibblesOf(comp[40:]), n)
			return nil
		}

		// Stream MDBX live slots — no full-set map. A slot deleted by the
		// window net is no longer live (skip). A slot also written this window
		// is still live: emit here ONCE and drop it from winPend so the tail
		// loop below doesn't double-emit it (a duplicate would append a
		// redundant chgEvent in recordChangeStorage).
		c, cerr := tx.CursorDupSort(modules.HashedStorage)
		if cerr != nil {
			return cerr
		}
		// HashedStorage is AutoDupSort with DupToLen=32: the physical DupSort key is
		// the 32-byte addrHash (incarnation removed), and each dup value is
		// slotHash32||slotValue. SeekBothRange must use the 32-byte addrHash — NOT
		// the 40-byte leaf-domain prefix (addrHash+8-byte incarnation), which never
		// matches a physical key and returns zero rows, silently dropping the
		// tombstones for every slot that pre-existed this window (the leaf-history
		// fold would then resurrect them after the SELFDESTRUCT). The 40-byte domain
		// is still used to build the 72-byte DatcLeafS composite key below.
		for v, e := c.SeekBothRange(ah[:], nil); v != nil && e == nil; _, v, e = c.NextDup() {
			if len(v) < 32 {
				continue
			}
			var sh [32]byte
			copy(sh[:], v[:32])
			if winDel[sh] {
				continue
			}
			delete(winPend, sh)
			if err := emit(sh); err != nil {
				c.Close()
				return err
			}
		}
		c.Close()
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
	// Read the boundary header first so we can arm the concurrent-root gold check:
	// if --concurrent-root's parallel combine diverges from header.Root, the
	// computer dumps a per-nibble diagnostic and transparently recomputes the
	// window with the serial loader (the trusted oracle) instead of crashing. A
	// genuine data/header mismatch (serial also wrong) still surfaces below.
	hdr, err := b.hdrs.ReadHeader(n)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	root := b.lastRoot
	if len(b.winA) > 0 || len(b.winS) > 0 {
		if b.concurrentRoot {
			trc.SetExpectRoot(hdr.Root)
		}
		r, err := trc.ComputeRoot(b.winA, b.winS)
		trc.ClearExpectRoot()
		if err != nil {
			return fmt.Errorf("window ComputeRoot →%d: %w", n, err)
		}
		root = r
		b.winA = make(map[types.Address]*account.StateAccount, 64)
		b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
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
		// HashedStorage is AutoDupSort with DupToLen=32: the physical DupSort key is
		// the 32-byte addrHash (incarnation removed) and each dup value is
		// slotHash32||slotValue. SeekBothRange must use the 32-byte addrHash; the
		// earlier 40-byte prefix (addrHash+8-byte incarnation) never matches a
		// physical key and returned zero rows, silently dropping the per-slot
		// tombstones for a SELFDESTRUCT-ed contract (the leaf-history fold then
		// resurrects pre-destruct slots at later heights). The 40-byte domain is
		// still used to build the 72-byte DatcLeafS composite key.
		c, cerr := tx.CursorDupSort(modules.HashedStorage)
		if cerr != nil {
			return cerr
		}
		prefix := make([]byte, 40)
		copy(prefix, ah[:])
		for v, e := c.SeekBothRange(ah[:], nil); v != nil && e == nil; _, v, e = c.NextDup() {
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

	if !b.windowing {
		if b.stoRootEmits == nil {
			b.stoRootEmits = make(map[string]types.Hash, 64)
		} else {
			clear(b.stoRootEmits)
		}
	}
	// Arm the expected root BEFORE the fold so the concurrent path can gold-check
	// its combine against the header and self-heal via the serial fallback (the
	// AccRootEmitter then re-fires from the serial loader — no double emits).
	var expectRootSet bool
	var expectRoot types.Hash
	if !b.fwdMode && b.concurrentRoot {
		hdr, herr := b.hdrs.ReadHeader(n)
		if herr != nil {
			return fmt.Errorf("read header: %w", herr)
		}
		expectRoot, expectRootSet = hdr.Root, true
		trc.SetExpectRoot(hdr.Root)
		defer trc.ClearExpectRoot()
	}
	root, err := trc.ComputeRoot(dirtyA, dirtyS)
	if err != nil {
		return fmt.Errorf("ComputeRoot: %w", err)
	}
	if !b.windowing && len(dirtyS) > 0 {
		var rk8 [8]byte
		binary.BigEndian.PutUint64(rk8[:], n)
		for addr := range dirtyS {
			ah := b.addrHash(addr)
			k := make([]byte, 40)
			copy(k, ah[:])
			copy(k[32:], rk8[:])
			var v []byte
			if r, ok := b.stoRootEmits[string(ah[:])]; ok {
				v = append([]byte{}, r[:]...)
			}
			b.stoRootBuf = append(b.stoRootBuf, kvPair{k: k, v: v})
		}
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
		want := expectRoot
		if !expectRootSet {
			hdr, herr := b.hdrs.ReadHeader(n)
			if herr != nil {
				return fmt.Errorf("read header: %w", herr)
			}
			want = hdr.Root
		}
		if root != want {
			return fmt.Errorf("ROOT MISMATCH: computed %x != header %x", root, want)
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
