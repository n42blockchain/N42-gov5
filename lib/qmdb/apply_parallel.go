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
func (t *Tree) precompute(ops []Op, pre []preOp, lo, hi int, undo bool) {
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
		if len(ops[i].Value) != 0 {
			p.leaf = hashLeaf(kh, ops[i].Value)
		}
	}
}

// applyOpsParallel is ApplyOps with the pre-pass fanned out over `workers`.
func (t *Tree) applyOpsParallel(ops []Op, workers int) {
	pre := make([]preOp, len(ops))
	undo := t.rec != nil
	chunks := (len(ops) + parallelApplyMinChunk - 1) / parallelApplyMinChunk
	if workers > chunks {
		workers = chunks
	}
	if workers <= 1 {
		t.precompute(ops, pre, 0, len(ops), undo)
	} else {
		var wg sync.WaitGroup
		per := (len(ops) + workers - 1) / workers
		for lo := 0; lo < len(ops); lo += per {
			hi := min(lo+per, len(ops))
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				t.precompute(ops, pre, lo, hi, undo)
			}(lo, hi)
		}
		wg.Wait()
	}

	// Ordered commit: Set/Delete with the precomputed inputs.
	t.BeginLeafBatch()
	for i := range ops {
		p := &pre[i]
		kh := ops[i].KeyHash
		if p.hasOld {
			if p.undoOK {
				t.rec.Entries = append(t.rec.Entries, UndoEntry{Slot: p.old, KeyHash: kh, Value: p.undoVal})
			} else {
				t.recordDeactivation(p.old, kh)
			}
			t.deactivate(p.old)
		}
		if len(ops[i].Value) == 0 {
			if p.hasOld {
				t.idx.Delete(kh)
			}
			continue
		}
		t.appendLive(kh, ops[i].Value, p.leaf)
	}
	t.EndLeafBatch()
	t.Root()
}

// appendLive is the append half of Set with the leaf hash supplied.
func (t *Tree) appendLive(keyHash Hash, value []byte, leaf Hash) {
	slot := t.nextSlot
	t.nextSlot++
	if t.rec != nil {
		t.rec.AppendedKeys = append(t.rec.AppendedKeys, keyHash)
	}
	tw := t.twigFor(slot)
	local := slot % TwigSize
	id := int(slot / TwigSize)
	v := t.allocVal(value)
	t.setEntry(slot, entry{keyHash: keyHash, value: v, active: true})
	t.writeLeaf(tw, id, local, leaf)
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
