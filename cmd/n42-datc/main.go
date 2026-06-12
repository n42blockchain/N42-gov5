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
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		DirtySpace(uint64(16 * datasize.GB)).
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

		chgStoAgg: make(map[string]*[]chgEvent, 1<<14),
		outDir:    *out,
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
