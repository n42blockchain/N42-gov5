// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Batched leaf folding. The eager setLeaf path hashes the full 11-node twig
// path on EVERY leaf write; within a block batch those paths overlap heavily —
// appends land in consecutive slots, so a 2048-leaf contiguous fill shares all
// internal nodes (2047 hashes total instead of 11×2048 = 22528). BeginLeafBatch
// defers folding: leaf writes only record their position, and EndLeafBatch (or
// any Root/Proof/Flush) folds the UNION of ancestor paths per twig, level by
// level with parent dedup. The fold computes the exact same node values from
// the same leaves, so the root is byte-identical to the eager path.

package qmdb

import "sort"

// writeLeaf writes a leaf hash, folding eagerly or deferring to the batch.
// Both paths leave identical leaf state; only WHEN internal nodes fold differs.
func (t *Tree) writeLeaf(tw *twig, id int, local uint64, h Hash) {
	if !t.batchFolding || tw.dirty {
		// Eager fold (or dirty twig: recompute will rebuild everything anyway).
		tw.setLeaf(local, h)
		return
	}
	tw.nodes[TwigSize+local] = h
	t.batchTouched = append(t.batchTouched, uint64(id)*TwigSize+local)
}

// BeginLeafBatch defers internal-node folding for subsequent Set/Delete calls
// until EndLeafBatch (or the next Root/GetProof/FlushTo, which settle pending
// leaves automatically). Single-threaded like the rest of Tree.
func (t *Tree) BeginLeafBatch() { t.batchFolding = true }

// EndLeafBatch exits batch mode and folds all deferred leaf paths.
func (t *Tree) EndLeafBatch() {
	t.batchFolding = false
	t.foldTouched()
}

// foldTouched folds the deferred leaves' ancestor paths, grouped by twig, with
// per-level parent dedup. Safe to call mid-batch (it just settles what is
// pending so far).
func (t *Tree) foldTouched() {
	if len(t.batchTouched) == 0 {
		return
	}
	touched := t.batchTouched
	sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
	for i := 0; i < len(touched); {
		id := int(touched[i] / TwigSize)
		j := i
		for j < len(touched) && int(touched[j]/TwigSize) == id {
			j++
		}
		tw := t.twigs[id]
		// A twig that went dirty after its leaves were recorded is rebuilt in
		// full by recompute; an evicted/pruned twig cannot have pending leaves
		// (writes hydrate first), but guard anyway.
		if !tw.dirty && tw.nodes != nil {
			t.foldTwigTouched(tw, touched[i:j])
		}
		i = j
	}
	t.batchTouched = t.batchTouched[:0]
}

// foldTwigTouched recomputes exactly the union of ancestor paths of the given
// (sorted, same-twig) leaf slots, bottom-up with duplicate-parent elision.
func (t *Tree) foldTwigTouched(tw *twig, touched []uint64) {
	idxs := t.batchScratch[:0]
	last := uint32(0)
	for _, p := range touched {
		j := uint32(TwigSize + p%TwigSize)
		if j == last { // same leaf written twice in the batch
			continue
		}
		last = j
		idxs = append(idxs, j)
	}
	// Fold level by level: parents of a sorted index list are sorted, so dedup
	// is a single adjacent comparison.
	for idxs[0] >= 2 {
		w := 0
		lastParent := uint32(0)
		for _, j := range idxs {
			p := j >> 1
			if p == lastParent {
				continue
			}
			lastParent = p
			tw.nodes[p] = hashNode(tw.nodes[2*p], tw.nodes[2*p+1])
			idxs[w] = p
			w++
		}
		idxs = idxs[:w]
	}
	tw.root = tw.nodes[1]
	t.batchScratch = idxs[:0]
}
