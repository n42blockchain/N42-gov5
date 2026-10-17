// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Parallel apply (S72). ApplyOps' sequential loop pays, per op, a dependent
// index probe into a multi-million-key table, a copy of the deactivated
// entry's value for the undo record, and the leaf hash. None of those three
// depends on any other op of the same batch when the batch's keys are
// distinct: the probe of key k returns the same slot before and during the
// batch (only op k itself rewrites k's mapping), the old entry's value is
// immutable (deactivation only flips its liveness), and the leaf hash is a
// pure function of (keyHash, value). So they are computed up front by N
// workers over contiguous chunks of the op list, into per-op result slots.
//
// Every mutation stays on the calling goroutine, in op order, exactly as the
// sequential path performs it: slot assignment (nextSlot), undo Entries and
// AppendedKeys appends, deadFlushed appends, history death stamps and key
// versions, arena copies, twig growth, liveness-bit flips, touched-leaf and
// touched-bits bookkeeping, and index Put/Delete. The commit loop below is
// Set/Delete with the three precomputed inputs substituted, so the root, the
// undo record and the persisted rows are the same bytes as the sequential
// path's. Batches that do not satisfy the preconditions (duplicate or
// unsorted keys, an index other than the two pure-read in-RAM shapes) run the
// sequential path unchanged.

package qmdb

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"sync"
)

// ParallelApplyWorkers is the worker count for ApplyOps' parallel pre-pass;
// 0 (the default, N42_QMDB_PARALLEL_APPLY unset or 0) keeps the sequential
// path byte-for-byte. Read once at init; tests set it directly.
var ParallelApplyWorkers = func() int {
	n, err := strconv.Atoi(os.Getenv("N42_QMDB_PARALLEL_APPLY"))
	if err != nil || n < 0 {
		return 0
	}
	if n > 256 {
		n = 256
	}
	return n
}()

// parallelApplyMinChunk is the smallest op range worth a goroutine.
const parallelApplyMinChunk = 512

// preOp is one op's precomputed, order-independent inputs.
type preOp struct {
	old     uint64 // live slot before the batch (valid iff hasOld)
	leaf    Hash   // hashLeaf(key, value) for a set
	undoVal []byte // copy of the old entry's value (resident only)
	hasOld  bool
	undoOK  bool // undoVal is filled; otherwise recordDeactivation reads it
}

// parallelApplyEligible reports whether a batch can take the parallel path:
// a pure-read in-RAM index and strictly ascending (hence distinct) keys.
func (t *Tree) parallelApplyEligible(ops []Op) bool {
	switch t.idx.(type) {
	case *flatIndex, mapIndex:
	default:
		return false
	}
	for i := 1; i < len(ops); i++ {
		if bytes.Compare(ops[i-1].KeyHash[:], ops[i].KeyHash[:]) >= 0 {
			return false
		}
	}
	return true
}

// precompute fills pre[lo:hi]. Read-only on the tree: the index (pure-read
// shapes only, see parallelApplyEligible) and the resident entry window.
// hashLeaves=false leaves the leaf hash to the commit (writeLeafEntry, which
// defers it to the batch fold's 16-wide pass under AVX-512).
func (t *Tree) precompute(ops []Op, pre []preOp, lo, hi int, undo, hashLeaves bool) {
	if fi, ok := t.idx.(*flatIndex); ok {
		// Warm the probe lines first so the dependent misses overlap.
		for i := lo; i < hi; i++ {
			fi.prefetch(ops[i].KeyHash)
		}
	}
	for i := lo; i < hi; i++ {
		p := &pre[i]
		kh := ops[i].KeyHash
		p.old, p.hasOld = t.idx.Get(kh)
		if undo && p.hasOld && p.old >= t.entriesBase {
			if j := p.old - t.entriesBase; j < uint64(len(t.entries)) {
				v := t.entries[j].value
				p.undoVal = make([]byte, len(v))
				copy(p.undoVal, v)
				p.undoOK = true
			}
		}
		if hashLeaves && len(ops[i].Value) != 0 {
			p.leaf = hashLeaf(kh, ops[i].Value)
		}
	}
}

// PreparedOps is a sorted, distinct-key op batch whose order-independent
// inputs (old slots, undo copies, optionally leaf hashes) were computed by
// PrepareOps without mutating the tree. CommitPrepared applies it; the tree
// must not change in between (the caller serialises its writers; the cursor
// is checked as a guard).
type PreparedOps struct {
	ops        []Op
	pre        []preOp
	nextSlot   uint64
	hashLeaves bool

	// Allocations the commit would otherwise make while it holds the tree,
	// done ahead from read-only state (the caller excludes other mutators):
	// vals are the set values copied into one batch chunk (allocVal's arena
	// semantics: views append-ordered like their slots); entries is the
	// resident window copied into a backing array that fits the batch's
	// appends (nil when it already does); idx is the flat index rehashed to
	// take the batch's inserts without a rehash (nil when it already can).
	vals    [][]byte
	entries []entry
	entLen  int
	idx     *flatIndex
}

// ErrPreparedStale is CommitPrepared's refusal of a batch prepared against a
// tree that has changed since (nothing was applied).
var ErrPreparedStale = errors.New("qmdb: prepared ops are stale (tree changed since PrepareOps)")

// CanPrepare reports whether ops can take PrepareOps/CommitPrepared: a
// pure-read in-RAM index and strictly ascending keys.
func (t *Tree) CanPrepare(ops []Op) bool { return t.parallelApplyEligible(ops) }

// PrepareOps computes ops' order-independent inputs on `workers` goroutines.
// It only READS the tree (index and resident entries), so it may run
// concurrently with other readers -- but not with any mutation. undo says
// whether the commit will run under undo recording (the deactivated values
// are copied here). hashLeaves computes the leaf hashes here instead of in
// the commit/fold. Returns nil if !CanPrepare(ops).
func (t *Tree) PrepareOps(ops []Op, undo bool, workers int, hashLeaves bool) *PreparedOps {
	if !t.parallelApplyEligible(ops) {
		return nil
	}
	pre := make([]preOp, len(ops))
	chunks := (len(ops) + parallelApplyMinChunk - 1) / parallelApplyMinChunk
	if workers > chunks {
		workers = chunks
	}
	if workers <= 1 {
		t.precompute(ops, pre, 0, len(ops), undo, hashLeaves)
	} else {
		var wg sync.WaitGroup
		per := (len(ops) + workers - 1) / workers
		for lo := 0; lo < len(ops); lo += per {
			hi := min(lo+per, len(ops))
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				t.precompute(ops, pre, lo, hi, undo, hashLeaves)
			}(lo, hi)
		}
		wg.Wait()
	}
	p := &PreparedOps{ops: ops, pre: pre, nextSlot: t.nextSlot, hashLeaves: hashLeaves}
	t.prepareAllocs(p)
	return p
}

// prepareAllocs makes the commit's allocations ahead (PreparedOps).
func (t *Tree) prepareAllocs(p *PreparedOps) {
	sets, inserts, bytesTotal := 0, 0, 0
	for i := range p.ops {
		if n := len(p.ops[i].Value); n != 0 {
			sets++
			bytesTotal += n
			if !p.pre[i].hasOld {
				inserts++
			}
		}
	}
	if sets == 0 {
		return
	}
	buf := make([]byte, bytesTotal)
	p.vals = make([][]byte, len(p.ops))
	off := 0
	for i := range p.ops {
		if v := p.ops[i].Value; len(v) != 0 {
			n := copy(buf[off:], v)
			p.vals[i] = buf[off : off+n : off+n]
			off += n
		}
	}
	// setEntry places slot s at s-entriesBase; the batch ends at nextSlot+sets.
	if need := int(t.nextSlot-t.entriesBase) + sets; need > cap(t.entries) {
		e := make([]entry, need+need/4)
		copy(e, t.entries)
		// Touch the tail too: a fresh large allocation is untouched OS
		// memory, and its first-write page faults would land in the commit.
		clear(e[len(t.entries):])
		p.entries, p.entLen = e[:len(t.entries)], len(t.entries)
	}
	if fi, ok := t.idx.(*flatIndex); ok {
		p.idx = fi.grownFor(inserts)
	}
}

// CommitPrepared runs the ordered mutation half of ApplyOps for a prepared
// batch: slot assignment, undo/death bookkeeping, appends, liveness bits and
// index updates -- everything a value read (Get/GetVia) observes. It opens
// the leaf batch and leaves it open: the leaf hashing and the twig/upper
// fold are FinishBatch's, which touches no state a value read uses.
func (t *Tree) CommitPrepared(p *PreparedOps) error {
	if p == nil || p.nextSlot != t.nextSlot {
		return ErrPreparedStale
	}
	if p.entries != nil {
		if len(t.entries) != p.entLen {
			return ErrPreparedStale
		}
		t.entries = p.entries
	}
	if p.idx != nil {
		t.idx = p.idx
	}
	t.BeginLeafBatch()
	for i := range p.ops {
		pp := &p.pre[i]
		kh := p.ops[i].KeyHash
		if pp.hasOld {
			if pp.undoOK && t.rec != nil {
				t.rec.Entries = append(t.rec.Entries, UndoEntry{Slot: pp.old, KeyHash: kh, Value: pp.undoVal})
			} else {
				t.recordDeactivation(pp.old, kh)
			}
			t.deactivate(pp.old)
		}
		if len(p.ops[i].Value) == 0 {
			if pp.hasOld {
				t.idx.Delete(kh)
			}
			continue
		}
		v := p.ops[i].Value
		if p.vals != nil {
			v = p.vals[i]
		}
		t.appendLiveView(kh, v, pp.leaf, p.hashLeaves)
	}
	return nil
}

// FinishBatch closes the leaf batch CommitPrepared opened -- deferred leaf
// hashes, the touched twig paths, the liveness-bitmap roots -- and folds the
// upper tree, returning the root. It writes only twig/upper/root state, never
// the index or the entry window, so value reads (GetVia) may run concurrently
// with it; anything reading twig state (Root, proofs) may not.
func (t *Tree) FinishBatch() Hash {
	t.EndLeafBatch()
	return t.Root()
}

// applyOpsParallel is ApplyOps with the pre-pass fanned out over `workers`.
func (t *Tree) applyOpsParallel(ops []Op, workers int) {
	p := t.PrepareOps(ops, t.rec != nil, workers, true)
	if err := t.CommitPrepared(p); err != nil {
		panic(err) // prepared against this very tree a moment ago
	}
	t.FinishBatch()
}

// appendLiveView is the append half of Set for a value already copied into
// stable storage (the arena, or a prepared batch chunk), with the leaf hash
// supplied (haveLeaf) or left to writeLeafEntry.
func (t *Tree) appendLiveView(keyHash Hash, v []byte, leaf Hash, haveLeaf bool) {
	slot := t.nextSlot
	t.nextSlot++
	if t.rec != nil {
		t.rec.AppendedKeys = append(t.rec.AppendedKeys, keyHash)
	}
	tw := t.twigFor(slot)
	local := slot % TwigSize
	id := int(slot / TwigSize)
	t.setEntry(slot, entry{keyHash: keyHash, value: v, active: true})
	if haveLeaf {
		t.writeLeaf(tw, id, local, leaf)
	} else {
		t.writeLeafEntry(tw, id, local, slot, keyHash, v)
	}
	t.setTwigBit(tw, id, local, true)
	tw.live++
	t.markUpperDirty(id)
	t.idx.Put(keyHash, slot)
	if t.hist != nil {
		if err := t.hist.store.AppendKeyVersion(keyHash, slot); err != nil {
			t.hist.appendErr = err
		}
	}
	t.rootDirty = true
}

// HaveAVX512 reports whether the leaf hashes run 16 per AVX-512 pass (the
// batch fold's deferred path).
func HaveAVX512() bool { return haveAVX512 }
