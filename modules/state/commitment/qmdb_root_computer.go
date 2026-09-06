// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// QMDBRootComputer adapts the append-only twig-forest prototype (lib/qmdb) to
// the state.RootComputer interface, so replay-v2 can drive it like JMT/BMT/MPT.
// P1: in-memory only — validates that the QMDB layout produces a correct,
// deterministic world root from the per-block dirty set. P2 will back the twig
// log with the freezer and the index with MPHF.
//
// NOTE: the QMDB world root commits to live entries at their append slots, so it
// is a function of update history, not a canonical function of the key set. A
// from-live-key-set rebuild yields a different root by design; snapshot-sync must
// ship slot positions (qmdb.SnapshotLog / FromSnapshotLog).

package commitment

import (
	"bytes"
	"errors"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// QMDBRootComputer maintains a qmdb.Tree across blocks.
type QMDBRootComputer struct {
	t              *qmdb.Tree
	flushedThrough uint64         // entry-log slots already persisted (for incremental flush)
	stagedFlushed  uint64         // FlushTo's advanced cursor, adopted only by CommitFlushed
	stagedValid    bool           // stagedFlushed holds a not-yet-committed FlushTo
	mdbxIdx        *qmdbMDBXIndex // non-nil when the live-key index is MDBX-backed
	indexTrusted   uint64         // slot cursor below which the in-RAM index matches the store (ReloadForBuild fast path); 0 = never fully loaded
	indexDelta     int            // live-bits minus index-size baseline after the last full rebuild (fossil slots; see LoadFromTrustedIndex)

	undoRecording bool            // capture per-block undo data in ComputeRoot
	lastUndo      *qmdb.BlockUndo // undo record of the most recent ComputeRoot

	histStore *MDBXQMDBHistoryStore // non-nil when full-history journaling is on

	// readers serialises out-of-band point reads (Lookup: the txpool, RPC
	// and the miner's build under N42_STATE_WRITE_QMDB_ONLY) against the
	// owner's mutations. Every method that changes the tree takes it
	// exclusively for its own duration; the owner is one goroutine and calls
	// them in sequence, so the tree a reader sees between two of them is a
	// whole applied state -- the head as executed, possibly not yet
	// persisted, the same thing the layered cache already exposes.
	// Uncontended when no source is installed.
	readers sync.RWMutex
}

// Lookup reads the live value for keyHash from another goroutine. cold is
// the reader's own transaction (nil to use the owner's attached one, which
// is only safe from the owner). It returns evicted=true when the live entry
// sits below the resident window and cold cannot see it -- the caller's
// transaction predates the flush that wrote it -- so the caller can retry
// with a fresh transaction.
func (r *QMDBRootComputer) Lookup(keyHash qmdb.Hash, cold qmdb.Getter) (value []byte, found bool, evicted bool) {
	t0 := time.Now()
	r.readers.RLock()
	if w := time.Since(t0); w > 50*time.Microsecond {
		qmdbLookupWaitNanos.Add(w.Nanoseconds())
	}
	defer r.readers.RUnlock()
	var cr qmdb.ColdReader = noColdReader{}
	if cold != nil {
		cr = qmdb.ColdReaderFromGetter(cold)
	}
	return r.t.GetVia(keyHash, cr)
}

// LockReaders takes the tree's reader lock for a span of lookups -- a
// Block-STM execution, whose workers would otherwise take and release it per
// read (an atomic add on one shared cache line, 32 workers wide). The tree
// is static for the span: its owner is the caller, and it is waiting. The
// returned func releases the lock.
func (r *QMDBRootComputer) LockReaders() func() {
	r.readers.RLock()
	return r.readers.RUnlock
}

// LookupLocked is Lookup for a caller inside a LockReaders span.
func (r *QMDBRootComputer) LookupLocked(keyHash qmdb.Hash, cold qmdb.Getter) (value []byte, found bool, evicted bool) {
	var cr qmdb.ColdReader = noColdReader{}
	if cold != nil {
		cr = qmdb.ColdReaderFromGetter(cold)
	}
	return r.t.GetVia(keyHash, cr)
}

// qmdbLookupWaitNanos accumulates time Lookup spent waiting for the tree's
// owner (round 28 diagnostic: the leader's build against the import's
// ComputeRoot/FlushTo at 163k-transaction blocks).
var qmdbLookupWaitNanos atomic.Int64

// QMDBLookupWaitNanos reports the process-wide accumulated Lookup wait.
func QMDBLookupWaitNanos() int64 { return qmdbLookupWaitNanos.Load() }

// noColdReader sees nothing, so a Lookup without a transaction reports an
// evicted entry as evicted instead of faulting through the owner's attached
// reader from a foreign goroutine.
type noColdReader struct{}

func (noColdReader) ColdEntry(uint64) (qmdb.Hash, []byte, bool) { return qmdb.Hash{}, nil, false }

// EnableHistory attaches the full-history recorder (death stamps, key
// versions, top band — lib/qmdb/history.go) writing through tx. Re-point the
// tx each batch with SetHistoryTx. Forces archival entry-row retention.
func (r *QMDBRootComputer) EnableHistory(tx kv.Tx) {
	r.histStore = NewMDBXQMDBHistoryStore(tx)
	r.t.SetHistoryRecorder(qmdb.NewHistoryRecorder(r.histStore))
}

// SetHistoryTx re-points the history store at the current batch's tx.
func (r *QMDBRootComputer) SetHistoryTx(tx kv.Tx) {
	if r.histStore != nil {
		r.histStore.SetTx(tx)
	}
}

// BeginBlockHistory / EndBlockHistory bracket one block's history journaling.
func (r *QMDBRootComputer) BeginBlockHistory(block uint64) error { return r.t.BeginBlock(block) }
func (r *QMDBRootComputer) EndBlockHistory() error               { return r.t.EndBlock() }

// ProofAtHeight reconstructs a membership proof + root at height h from the
// journaled history. The caller verifies the root against the block header.
func (r *QMDBRootComputer) ProofAtHeight(keyHash qmdb.Hash, h uint64) (*qmdb.Proof, qmdb.Hash, bool, error) {
	return r.t.ProofAtHeight(keyHash, h)
}

// EnableUndoRecording makes every subsequent ComputeRoot capture the block's
// undo record (for the recent-window historical proofs); fetch it with
// LastUndo right after the ComputeRoot call.
func (r *QMDBRootComputer) EnableUndoRecording() { r.undoRecording = true }

// RevertBlock rolls the live QMDB tree back across one block using its undo
// record, repairing the persisted positional layout in the same tx and
// adopting the rewound flush cursor. This is the state half of a HotStuff
// same-height sibling switch: the QMDB root is append-history-dependent, so a
// competing block MUST be re-executed on the reverted tree — executing it on
// top of the un-reverted one appends at shifted slots and forks the root
// permanently vs nodes that only ever applied the winner.
//
// Call with the most recently applied block's undo first (newest→oldest for
// deeper unwinds). tx must be the same RwTx the subsequent re-execution
// flushes through.
func (r *QMDBRootComputer) RevertBlock(tx kv.RwTx, undo *qmdb.BlockUndo) error {
	r.readers.Lock()
	defer r.readers.Unlock()
	// Re-point the cold entry reader + leaf store at THIS tx first: the tree's
	// attached readers still reference whatever tx the last execution ran with
	// (evmRecord re-points them per block, then closes that tx) — reading
	// through a closed tx segfaults inside MDBX cursor open.
	r.setColdLocked(tx)
	if r.mdbxIdx != nil {
		r.mdbxIdx.setTx(tx)
	}
	// Any not-yet-consumed lastUndo describes appends relative to the tree
	// position this revert is about to abandon; replaying it later (the
	// dangling-candidate peel) would double-revert against the rewound tree.
	// The caller peels dangling appends BEFORE reverting, so at this point a
	// leftover record is stale by construction — drop it. A staged flush from
	// that abandoned position is equally stale.
	r.lastUndo = nil
	r.abortFlushedLocked()
	ft, err := r.t.ApplyUndoWithStorage(tx, undo, r.flushedThrough)
	if err != nil {
		return err
	}
	r.flushedThrough = ft
	return nil
}

// LastUndo returns the undo record captured by the most recent ComputeRoot
// (nil if recording is disabled or no block was computed yet).
func (r *QMDBRootComputer) LastUndo() *qmdb.BlockUndo { return r.lastUndo }

// TakeUndo returns the most recent ComputeRoot's undo record and clears it, so
// a caller persisting one record per block can detect a block whose root was
// never recomputed (nil = state unchanged → synthesize an empty record) instead
// of re-writing a stale one.
func (r *QMDBRootComputer) TakeUndo() *qmdb.BlockUndo {
	r.readers.Lock()
	defer r.readers.Unlock()
	u := r.lastUndo
	r.lastUndo = nil
	return u
}

// NewQMDBRootComputer creates an empty in-memory QMDB root computer.
func NewQMDBRootComputer() *QMDBRootComputer {
	return &QMDBRootComputer{t: qmdb.New()}
}

// VoidIndexTrust marks the in-RAM index untrusted, forcing the next
// ReloadForBuild to rebuild it from the entry log (used when a candidate
// peel failed and the index may carry unpeeled mutations).
func (r *QMDBRootComputer) VoidIndexTrust() { r.indexTrusted = 0 }

// ApplyUndo peels a block's appends off the tree under the reader lock.
func (r *QMDBRootComputer) ApplyUndo(undo *qmdb.BlockUndo) error {
	r.readers.Lock()
	defer r.readers.Unlock()
	return r.t.ApplyUndo(undo)
}

// Tree exposes the underlying tree (for snapshot/proof tests).
func (r *QMDBRootComputer) Tree() *qmdb.Tree { return r.t }

// Root returns the current world root without applying a dirty set.
func (r *QMDBRootComputer) Root() types.Hash {
	r.readers.Lock()
	defer r.readers.Unlock()
	return types.Hash(r.t.Root())
}

// LoadFrom rebuilds the forest from a previously-flushed positional layout (the
// cross-process resume path — the QMDB root is history-dependent, so resume must
// replay positions, not rebuild from the key set). flushedThrough advances to the
// reloaded cursor so the next flush is incremental.
func (r *QMDBRootComputer) LoadFrom(g qmdb.Getter) error {
	r.readers.Lock()
	defer r.readers.Unlock()
	return r.loadFromLocked(g)
}

func (r *QMDBRootComputer) loadFromLocked(g qmdb.Getter) error {
	if r.mdbxIdx == nil {
		// In-RAM index: force the rebuild scan. On a mid-run recovery reload
		// the index still reflects the PRE-reload in-memory tree (e.g. reverts
		// a rolled-back tx discarded); Tree.LoadFrom's non-empty-index fast
		// path would keep those stale mappings and later overwrites would
		// deactivate the wrong slots — observed live as cross-node activeBits
		// divergence. The persistent MDBX index is transactional with the
		// store, so it stays consistent and must NOT be rescanned.
		r.t.SetIndex(qmdb.NewIndexFor(r.t, 0))
	}
	if err := r.t.LoadFrom(g); err != nil {
		return err
	}
	r.flushedThrough = r.t.NextSlot()
	r.stagedValid = false // any staged flush described the abandoned layout
	r.indexTrusted = r.t.NextSlot()
	// Baseline for trusted-reload reconciliation: fossil live bits (rows lost
	// to the fixed deadFlushed carryover bug) make this nonzero fleet-wide;
	// the reload invariant is that it stays CONSTANT.
	r.indexDelta = r.t.LiveBits() - r.t.LiveCount()
	return nil
}

// reloadVerify enables the paranoid cross-check: after every fast reload, run an
// INDEPENDENT full rebuild and assert the two agree. Costs a multi-second full
// load per build — diagnostic only, never on by default.
var reloadVerify = os.Getenv("N42_QMDB_RELOAD_VERIFY") == "1"

// ReloadForBuild reloads a PERSISTENT speculative-build computer against the
// store's current layout. A fresh computer pays a full index rescan on every
// build (observed live: ~5s in LoadFrom -> IO-bound, invisible on CPU profiles
// -> chronic view timeouts), and even the sealed-twig O(meta) reload rebuilds
// the WHOLE forest every block — a cost that grows with total history, measured
// at ~0.5s idle / ~2.75s under load while executing the block itself took
// ~0.17s. So the reload runs in three tiers, cheapest first:
//
//  1. incremental — advance the loaded forest across the delta only, and prove
//     it by reproducing the world root persisted with the twig metadata;
//  2. meta-scan   — rebuild the forest from twig metadata, trusting the index
//     below the previous cursor;
//  3. full rebuild — fresh index, rescan the entry log.
//
// Contract: between calls, the ONLY tree mutations must be candidate-build
// ComputeRoot ops, and they must have been peeled back with ApplyUndo before
// this call — then the in-RAM index is exact for every slot below the
// previous load's cursor and only the delta needs scanning. Any doubt
// (peel failure, reconciliation mismatch, root mismatch, first use) drops to the
// next tier automatically, so a tier-1 miss costs latency, never correctness.
func (r *QMDBRootComputer) ReloadForBuild(g qmdb.Getter) error {
	r.readers.Lock()
	defer r.readers.Unlock()
	if r.mdbxIdx != nil || r.indexTrusted == 0 {
		return r.loadFromLocked(g) // persistent index / first load: already right
	}
	prev := r.indexTrusted
	// Tier 1: advance the forest incrementally — cost proportional to what
	// changed since the last build, not to the length of the chain's history.
	// It self-verifies against the world root persisted next to the twig
	// metadata, so a decline/mismatch can only cost a fallback, never a wrong
	// root.
	t0 := time.Now()
	err := r.t.LoadIncremental(g, prev, r.indexDelta)
	if err == nil {
		r.afterReload(g, "incremental", t0)
		return nil
	}
	if errors.Is(err, qmdb.ErrIncrementalUnavailable) {
		log.Debug("qmdb incremental speculative reload declined", "reason", err)
	} else {
		// The delta scan could not account for everything that moved (in-window
		// delete, revert, compaction). Keep it LOUD: it is the signal that the
		// incremental path stopped carrying the fleet.
		log.Warn("qmdb incremental speculative reload rejected; falling back to the full-forest reload",
			"reason", err, "attempt", time.Since(t0))
	}
	// Tier 2: rebuild the whole forest from twig metadata, trusting the index
	// below the previous cursor (O(twigs) point reads, seconds at chain scale).
	t1 := time.Now()
	if err := r.t.LoadFromTrustedIndex(g, prev, r.indexDelta); err != nil {
		// Tier 3 — stale or inconsistent index (in-window delete, revert window,
		// unpeeled mutation): rebuild from scratch. This is the multi-second
		// path - make it VISIBLE (a silent fallback here was an observation
		// blind spot during the dropped-seal hunt).
		log.Warn("qmdb speculative reload fell back to a full index rebuild",
			"reason", err, "fastAttempt", time.Since(t1))
		t2 := time.Now()
		r.t.SetIndex(qmdb.NewIndexFor(r.t, 0))
		if err2 := r.t.LoadFrom(g); err2 != nil {
			return err2
		}
		log.Warn("qmdb speculative full rebuild done", "elapsed", time.Since(t2))
		r.afterReload(g, "full-rebuild", t1)
		return nil
	}
	r.afterReload(g, "meta-scan", t1)
	return nil
}

// afterReload adopts the cursors a completed reload established and, when the
// verify mode is armed, cross-checks the result against an independent rebuild.
func (r *QMDBRootComputer) afterReload(g qmdb.Getter, path string, since time.Time) {
	r.flushedThrough = r.t.NextSlot()
	r.stagedValid = false
	r.indexTrusted = r.t.NextSlot()
	r.indexDelta = r.t.LiveBits() - r.t.LiveCount()
	if reloadVerify {
		r.verifyReload(g, path, time.Since(since))
	}
}

// verifyReload rebuilds the forest from the same store into a THROWAWAY tree via
// the from-scratch path (fresh index, no trust boundary, no reuse) and asserts
// that the reload we just accepted agrees with it on the world root, the live-bit
// population, the forest geometry AND the full keyHash -> slot index mapping.
// Armed with N42_QMDB_RELOAD_VERIFY=1.
//
// This is the only way the incremental path earns trust on a live chain: it
// compares against the exact computation the incremental path is allowed to skip.
//
// The index comparison is not redundant with the root. The QMDB root commits to
// entries at their append slots, so an overwrite of A combined with a delete of
// B inside the same twig can reproduce the root byte for byte while the index
// still maps B to its dead slot — and Tree.Get resolves through the index
// without checking the live bit, so that stale mapping is a SILENT wrong read,
// not a detectable one. Cardinality alone cannot see it either: the stale entry
// keeps the count right. Only the mapping itself does.
func (r *QMDBRootComputer) verifyReload(g qmdb.Getter, path string, elapsed time.Duration) {
	t0 := time.Now()
	ref := qmdb.New() // fresh in-RAM index => LoadFrom does the full rescan
	ref.SetCold(qmdb.ColdReaderFromGetter(g))
	ref.SetLeafStore(qmdb.LeafStoreFromGetter(g))
	if err := ref.LoadFrom(g); err != nil {
		log.Error("QMDB RELOAD VERIFY: reference full rebuild failed", "path", path, "err", err)
		return
	}
	got, want := r.t.Root(), ref.Root()
	gotBits, wantBits := r.t.LiveBits(), ref.LiveBits()
	gotKeys, wantKeys := r.t.LiveCount(), ref.LiveCount()
	gotTwigs, wantTwigs := r.t.NumTwigs(), ref.NumTwigs()
	gotNext, wantNext := r.t.NextSlot(), ref.NextSlot()
	if got != want || gotBits != wantBits || gotKeys != wantKeys || gotTwigs != wantTwigs || gotNext != wantNext {
		log.Error("QMDB RELOAD VERIFY FAILED — reload diverged from an independent full rebuild",
			"path", path,
			"root", types.Hash(got), "wantRoot", types.Hash(want),
			"liveBits", gotBits, "wantLiveBits", wantBits,
			"indexKeys", gotKeys, "wantIndexKeys", wantKeys,
			"twigs", gotTwigs, "wantTwigs", wantTwigs,
			"nextSlot", gotNext, "wantNextSlot", wantNext)
		return
	}
	if !r.verifyReloadIndex(ref, path) {
		return
	}
	log.Info("QMDB RELOAD VERIFY ok", "path", path, "reload", elapsed, "referenceRebuild", time.Since(t0),
		"root", types.Hash(got), "twigs", gotTwigs, "nextSlot", gotNext, "indexKeys", gotKeys)
}

// verifyReloadIndex compares the reloaded tree's index against the reference
// rebuild's, mapping by mapping. The caller has already established that the two
// indexes hold the same NUMBER of keys, so checking that every reference mapping
// is present and identical in the reload proves the two sets are equal — an
// extra key in the reload would have to displace a reference one, which this
// walk reports. Returns false (and logs) on divergence.
func (r *QMDBRootComputer) verifyReloadIndex(ref *qmdb.Tree, path string) bool {
	const maxReported = 8
	var (
		missing  int
		mismatch int
		samples  []any
	)
	iterable := ref.IndexRange(func(keyHash qmdb.Hash, wantSlot uint64) bool {
		gotSlot, ok := r.t.IndexLookup(keyHash)
		switch {
		case !ok:
			missing++
		case gotSlot != wantSlot:
			mismatch++
		default:
			return true
		}
		if len(samples) < maxReported*6 { // 6 log fields per reported mapping
			samples = append(samples, "key", types.Hash(keyHash), "gotSlot", gotSlot, "wantSlot", wantSlot)
		}
		return true // keep counting: the totals say how deep the damage goes
	})
	if !iterable {
		// The MDBX-backed index is transactional with the store and is never
		// rebuilt by the reload tiers, so it is out of scope here — but say so
		// rather than let the "ok" line imply a check that did not run.
		log.Info("QMDB RELOAD VERIFY: index comparison skipped (non-iterable index)", "path", path)
		return true
	}
	if missing == 0 && mismatch == 0 {
		return true
	}
	log.Error("QMDB RELOAD VERIFY FAILED — index diverged from an independent full rebuild "+
		"(the world root matched, so this is exactly the class of stale mapping a root check cannot see)",
		append([]any{"path", path, "missing", missing, "mismatched", mismatch}, samples...)...)
	return false
}

// FlushTo persists entries appended since the last flush plus twig metadata and
// recovery meta (positional, sequential). Returns bytes written.
//
// The advanced flush cursor is only STAGED here: the writes live in p's
// transaction, which may still roll back. The caller must follow up with
// CommitFlushed once that tx commits (or AbortFlushed if it rolls back) —
// adopting the cursor before the tx is durable is how a rollback used to
// leave flushedThrough pointing past rows that never reached disk, silently
// skipping them on every later flush.
func (r *QMDBRootComputer) FlushTo(p qmdb.Putter) (int, error) {
	r.readers.Lock()
	defer r.readers.Unlock()
	// Settle the batch's accumulated death-stamp deltas first (one
	// read-modify-write per touched twig per batch), same tx as the flush.
	if err := r.t.FlushHistory(); err != nil {
		return 0, err
	}
	next, n, err := r.t.FlushTo(p, r.flushedThrough)
	if err != nil {
		return n, err
	}
	r.stagedFlushed = next
	r.stagedValid = true
	if r.mdbxIdx != nil {
		r.mdbxIdx.persistCount() // index rows are already written through the tx
	}
	return n, nil
}

// CommitFlushed adopts the cursor staged by the last FlushTo after its
// surrounding transaction committed. Call before EvictFlushed — eviction only
// covers slots below the ADOPTED cursor, so an uncommitted flush never has its
// rows dropped from RAM.
func (r *QMDBRootComputer) CommitFlushed() {
	r.readers.Lock()
	defer r.readers.Unlock()
	if r.stagedValid {
		r.flushedThrough = r.stagedFlushed
		r.stagedValid = false
	}
	r.t.CommitFlush()
}

// AbortFlushed discards the staged cursor after the surrounding transaction
// rolled back and re-queues the tree's staged dead-row reclaims. Call BEFORE
// peeling the failed block's appends (TakeUndo + ApplyUndo) so the peel sees
// the restored bookkeeping.
func (r *QMDBRootComputer) AbortFlushed() {
	r.readers.Lock()
	defer r.readers.Unlock()
	r.abortFlushedLocked()
}

func (r *QMDBRootComputer) abortFlushedLocked() {
	r.stagedValid = false
	r.t.AbortFlush()
}

// SetCold attaches the cold entry source (the persisted positional log) so the
// tree can evict flushed entries from RAM and fault them back on demand. It also
// attaches the leaf-blob store (same backing tx) so an evicted twig rehydrates in
// one read. The engine re-points both at the current batch's tx each batch.
func (r *QMDBRootComputer) SetCold(g qmdb.Getter) {
	r.readers.Lock()
	defer r.readers.Unlock()
	r.setColdLocked(g)
}

func (r *QMDBRootComputer) setColdLocked(g qmdb.Getter) {
	if g == nil {
		// Detach: a getter wrapping an expired transaction is a delayed nil
		// panic on the next cold fault (observed live). Callers re-point at a
		// live tx before the next block executes.
		r.t.SetCold(nil)
		r.t.SetLeafStore(nil)
		return
	}
	r.t.SetCold(qmdb.ColdReaderFromGetter(g))
	r.t.SetLeafStore(qmdb.LeafStoreFromGetter(g))
}

// UseMDBXIndex backs the live-key index with an MDBX table (instead of the default
// in-RAM Go map), so the index does not grow the heap with the live key set and
// survives restarts. Call once on the fresh computer BEFORE LoadFrom, with the
// first batch's tx. Re-point per batch with SetIndexTx.
func (r *QMDBRootComputer) UseMDBXIndex(tx kv.RwTx) {
	r.readers.Lock()
	defer r.readers.Unlock()
	r.mdbxIdx = newQMDBMDBXIndex(tx)
	r.t.SetIndex(r.mdbxIdx)
}

// ValueSource exposes the live tree for point reads of account and storage
// values. The tree already holds every value the address-keyed `Account` table
// does -- EncodeAccountValue is account.MarshalV2(), the same bytes
// PlainStateWriter puts there -- so a reader over this returns identical data
// without the 16,096 random-keyed rows a block writes to that table.
//
// Reads are only valid while the computer's cold reader is pointed at a live
// transaction (SetCold), which evmRecord does per block, and only from the
// block pipeline's own goroutine: the tree is mutated by ComputeRoot at the end
// of the same block.
func (r *QMDBRootComputer) ValueSource() *qmdb.Tree { return r.t }

// SetIndexTx re-points the MDBX index at the current batch's tx (no-op for the
// in-RAM index). Call each batch alongside SetCold.
func (r *QMDBRootComputer) SetIndexTx(tx kv.RwTx) {
	r.readers.Lock()
	defer r.readers.Unlock()
	if r.mdbxIdx != nil {
		r.mdbxIdx.setTx(tx)
	}
}

// EvictFlushed drops, up to the flushed cursor and recoverable from cold, both
// the entry records AND the sealed twig leaf arrays from RAM — bounding the
// resident footprint to the unflushed window plus the active/touched twigs. Must
// be called after FlushTo and after SetCold.
func (r *QMDBRootComputer) EvictFlushed() {
	r.readers.Lock()
	defer r.readers.Unlock()
	r.t.EvictThrough(r.flushedThrough)
	r.t.EvictTwigsThrough(r.flushedThrough)
}

// ResidentTwigLeaves exposes how many twigs still hold their leaf array (for the
// engine's per-batch memory log).
func (r *QMDBRootComputer) ResidentTwigLeaves() int { return r.t.ResidentTwigLeaves() }

// RootScheme reports the QMDB twig-forest scheme.
func (*QMDBRootComputer) RootScheme() state.RootScheme { return state.RootSchemeQMDB }

// ComputeRoot applies the dirty accounts and storage to the twig forest and
// returns the new world root. Empty accounts and zero storage slots delete.
//
// QMDB is append-only and its root is a function of the APPLICATION ORDER (each
// Set consumes a new slot). Go map iteration is randomized, so applying the dirty
// maps directly would make the root non-reproducible across nodes/runs. We
// therefore collect every operation, sort by keyHash, and apply in that
// deterministic order — so any node replaying the same blocks produces the same
// append sequence and the same root (the cross-node-agreement requirement for a
// physical-layout-dependent commitment).
func (r *QMDBRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	r.readers.Lock()
	defer r.readers.Unlock()
	type op struct {
		kh    qmdb.Hash
		value []byte // nil => delete
	}
	ops := make([]op, 0, len(accounts)+len(storage))
	for addr, acct := range accounts {
		kh := qmdb.Hash(AccountKeyHash(addr))
		if acct == nil || isAccountEmpty(acct) {
			ops = append(ops, op{kh: kh, value: nil})
		} else {
			ops = append(ops, op{kh: kh, value: EncodeAccountValue(acct)})
		}
	}
	for addr, slots := range storage {
		for slot, val := range slots {
			kh := qmdb.Hash(StorageKeyHash(addr, slot))
			if val == nil || val.IsZero() {
				ops = append(ops, op{kh: kh, value: nil})
			} else {
				var buf [32]byte
				val.WriteToSlice(buf[:])
				v := make([]byte, 32)
				copy(v, buf[:])
				ops = append(ops, op{kh: kh, value: v})
			}
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		return bytes.Compare(ops[i].kh[:], ops[j].kh[:]) < 0
	})
	if r.undoRecording {
		r.t.StartUndoRecording()
	}
	tApply := time.Now()
	for _, o := range ops {
		if o.value == nil {
			r.t.Delete(o.kh)
		} else {
			r.t.Set(o.kh, o.value)
		}
	}
	if r.undoRecording {
		r.lastUndo = r.t.StopUndoRecording()
	}
	dApply := time.Since(tApply)
	tFold := time.Now()
	root := types.Hash(r.t.Root())
	// Split the two halves of a root computation.
	//
	// QMDB is a BINARY tree, not a Patricia trie: an entry is appended to the
	// next free slot, its leaf is Blake3(0x01||keyHash||value), 2048 leaves make
	// an 11-level binary twig, and the twig roots make the in-memory upper tree
	// (see the package doc in lib/qmdb/qmdb.go). "Apply" is Set/Delete writing
	// leaves and marking twigs dirty; "fold" is recomputeDirtyTwigs plus either
	// updateUpperPath per dirty twig or a full rebuildUpper.
	//
	// The leader pays BOTH halves twice per block -- once on the isolated
	// speculative tree during the build (58.3 ms measured) and again replaying
	// the same ops onto the live tree during the write (59.3 ms) -- and both are
	// on its critical path. Which half dominates decides whether that
	// duplication is attackable: appending the entries to the live tree is work
	// that has to happen, but folding them to a root THERE is arguably
	// redundant, since the root is already known from the isolated computation
	// and the write path only compares the two.
	//
	// Observability only; logged at the same threshold as the other block
	// phases, and only for blocks big enough to matter.
	if len(ops) > 1000 {
		log.Info("qmdb root phases", "ops", len(ops),
			"applyNs", dApply.Nanoseconds(), "foldNs", time.Since(tFold).Nanoseconds())
	}
	return root, nil
}
