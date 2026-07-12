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
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
)

// QMDBRootComputer maintains a qmdb.Tree across blocks.
type QMDBRootComputer struct {
	t              *qmdb.Tree
	flushedThrough uint64         // entry-log slots already persisted (for incremental flush)
	mdbxIdx        *qmdbMDBXIndex // non-nil when the live-key index is MDBX-backed
	indexTrusted   uint64         // slot cursor below which the in-RAM index matches the store (ReloadForBuild fast path); 0 = never fully loaded
	indexDelta     int            // live-bits minus index-size baseline after the last full rebuild (fossil slots; see LoadFromTrustedIndex)

	undoRecording bool            // capture per-block undo data in ComputeRoot
	lastUndo      *qmdb.BlockUndo // undo record of the most recent ComputeRoot

	histStore *MDBXQMDBHistoryStore // non-nil when full-history journaling is on
}

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
	// Re-point the cold entry reader + leaf store at THIS tx first: the tree's
	// attached readers still reference whatever tx the last execution ran with
	// (evmRecord re-points them per block, then closes that tx) — reading
	// through a closed tx segfaults inside MDBX cursor open.
	r.SetCold(tx)
	if r.mdbxIdx != nil {
		r.mdbxIdx.setTx(tx)
	}
	// Any not-yet-consumed lastUndo describes appends relative to the tree
	// position this revert is about to abandon; replaying it later (the
	// dangling-candidate peel) would double-revert against the rewound tree.
	// The caller peels dangling appends BEFORE reverting, so at this point a
	// leftover record is stale by construction — drop it.
	r.lastUndo = nil
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

// Tree exposes the underlying tree (for snapshot/proof tests).
func (r *QMDBRootComputer) Tree() *qmdb.Tree { return r.t }

// Root returns the current world root without applying a dirty set.
func (r *QMDBRootComputer) Root() types.Hash { return types.Hash(r.t.Root()) }

// LoadFrom rebuilds the forest from a previously-flushed positional layout (the
// cross-process resume path — the QMDB root is history-dependent, so resume must
// replay positions, not rebuild from the key set). flushedThrough advances to the
// reloaded cursor so the next flush is incremental.
func (r *QMDBRootComputer) LoadFrom(g qmdb.Getter) error {
	if r.mdbxIdx == nil {
		// In-RAM index: force the rebuild scan. On a mid-run recovery reload
		// the index still reflects the PRE-reload in-memory tree (e.g. reverts
		// a rolled-back tx discarded); Tree.LoadFrom's non-empty-index fast
		// path would keep those stale mappings and later overwrites would
		// deactivate the wrong slots — observed live as cross-node activeBits
		// divergence. The persistent MDBX index is transactional with the
		// store, so it stays consistent and must NOT be rescanned.
		r.t.SetIndex(qmdb.NewMapIndex())
	}
	if err := r.t.LoadFrom(g); err != nil {
		return err
	}
	r.flushedThrough = r.t.NextSlot()
	r.indexTrusted = r.t.NextSlot()
	// Baseline for trusted-reload reconciliation: fossil live bits (rows lost
	// to the fixed deadFlushed carryover bug) make this nonzero fleet-wide;
	// the reload invariant is that it stays CONSTANT.
	r.indexDelta = r.t.LiveBits() - r.t.LiveCount()
	return nil
}

// ReloadForBuild reloads a PERSISTENT speculative-build computer against the
// store's current layout, riding the sealed-twig O(meta) fast path instead of
// the multi-second full index rescan a fresh computer pays (observed live:
// every leader build burned ~5s in LoadFrom -> IO-bound, invisible on CPU
// profiles -> chronic view timeouts).
//
// Contract: between calls, the ONLY tree mutations must be candidate-build
// ComputeRoot ops, and they must have been peeled back with ApplyUndo before
// this call — then the in-RAM index is exact for every slot below the
// previous load's cursor and only the delta needs scanning. Any doubt
// (peel failure, reconciliation mismatch, first use) falls back to the full
// rebuild automatically.
func (r *QMDBRootComputer) ReloadForBuild(g qmdb.Getter) error {
	if r.mdbxIdx != nil || r.indexTrusted == 0 {
		return r.LoadFrom(g) // persistent index / first load: already right
	}
	if err := r.t.LoadFromTrustedIndex(g, r.indexTrusted, r.indexDelta); err != nil {
		// Stale or inconsistent index (in-window delete, revert window,
		// unpeeled mutation): rebuild from scratch.
		r.t.SetIndex(qmdb.NewMapIndex())
		if err2 := r.t.LoadFrom(g); err2 != nil {
			return err2
		}
	}
	r.flushedThrough = r.t.NextSlot()
	r.indexTrusted = r.t.NextSlot()
	return nil
}

// FlushTo persists entries appended since the last flush plus twig metadata and
// recovery meta (positional, sequential). Returns bytes written.
func (r *QMDBRootComputer) FlushTo(p qmdb.Putter) (int, error) {
	// Settle the batch's accumulated death-stamp deltas first (one
	// read-modify-write per touched twig per batch), same tx as the flush.
	if err := r.t.FlushHistory(); err != nil {
		return 0, err
	}
	next, n, err := r.t.FlushTo(p, r.flushedThrough)
	if err != nil {
		return n, err
	}
	r.flushedThrough = next
	if r.mdbxIdx != nil {
		r.mdbxIdx.persistCount() // index rows are already written through the tx
	}
	return n, nil
}

// SetCold attaches the cold entry source (the persisted positional log) so the
// tree can evict flushed entries from RAM and fault them back on demand. It also
// attaches the leaf-blob store (same backing tx) so an evicted twig rehydrates in
// one read. The engine re-points both at the current batch's tx each batch.
func (r *QMDBRootComputer) SetCold(g qmdb.Getter) {
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
	r.mdbxIdx = newQMDBMDBXIndex(tx)
	r.t.SetIndex(r.mdbxIdx)
}

// SetIndexTx re-points the MDBX index at the current batch's tx (no-op for the
// in-RAM index). Call each batch alongside SetCold.
func (r *QMDBRootComputer) SetIndexTx(tx kv.RwTx) {
	if r.mdbxIdx != nil {
		r.mdbxIdx.setTx(tx)
	}
}

// EvictFlushed drops, up to the flushed cursor and recoverable from cold, both
// the entry records AND the sealed twig leaf arrays from RAM — bounding the
// resident footprint to the unflushed window plus the active/touched twigs. Must
// be called after FlushTo and after SetCold.
func (r *QMDBRootComputer) EvictFlushed() {
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
	return types.Hash(r.t.Root()), nil
}
