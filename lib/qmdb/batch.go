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

import (
	"sort"
	"unsafe"
)

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

// ApplyOps applies a batch of ops (Value nil/empty = delete) with batched
// folding and a software-prefetch pre-pass, then folds the tree clean. The
// hot path of ShardedTree.ApplyBatch.
//
// The pre-pass warms, in order: the index probe lines, then (via a cheap
// now-warm Get) each op's OLD twig leaf line — the deactivation in the apply
// loop otherwise takes a dependent DRAM miss per op into the twig forest.
// Prefetching is only a hint, so a stale old slot (the same key written twice
// in one batch) is harmless: the apply loop re-resolves authoritatively.
func (t *Tree) ApplyOps(ops []Op) {
	if fi, ok := t.idx.(*flatIndex); ok {
		for i := range ops {
			fi.prefetch(ops[i].KeyHash)
		}
		for i := range ops {
			if old, ok := fi.Get(ops[i].KeyHash); ok {
				if id := int(old / TwigSize); id < len(t.twigs) {
					if tw := t.twigs[id]; tw.nodes != nil {
						prefetcht0(unsafe.Pointer(&tw.nodes[TwigSize+old%TwigSize]))
					}
				}
			}
		}
	}
	t.BeginLeafBatch()
	for i := range ops {
		if len(ops[i].Value) == 0 {
			t.Delete(ops[i].KeyHash)
		} else {
			t.Set(ops[i].KeyHash, ops[i].Value)
		}
	}
	t.EndLeafBatch()
	t.Root()
}

// foldTouched folds the deferred leaves' ancestor paths LEVEL-SYNCHRONIZED
// ACROSS TWIGS: each of the 11 levels collects every touched twig's parents
// into one list (packed twigID<<12 | nodeIdx) and hashes it in bulk. This is
// what lets SCATTERED updates (a churn block deactivates ~1 leaf in each of
// many old twigs) still fill 8-way SIMD groups — a per-twig fold would
// degenerate to 11 scalar hashes per twig. Consecutive same-twig runs (the
// append side) keep the zero-copy direct path. Safe to call mid-batch.
func (t *Tree) foldTouched() {
	if len(t.batchTouched) == 0 {
		return
	}
	touched := t.batchTouched
	sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
	// Pack to (twigID<<12 | heap index); skip dirty (recompute rebuilds them
	// in full) and evicted twigs (writes hydrate first; guard anyway), and
	// collapse duplicate slots.
	g := t.foldScratch[:0]
	for i := 0; i < len(touched); {
		id := int(touched[i] / TwigSize)
		j := i
		for j < len(touched) && int(touched[j]/TwigSize) == id {
			j++
		}
		tw := t.twigs[id]
		if !tw.dirty && tw.nodes != nil {
			last := ^uint64(0)
			for _, s := range touched[i:j] {
				v := uint64(id)<<12 | (TwigSize + s%TwigSize)
				if v != last {
					g = append(g, v)
					last = v
				}
			}
		}
		i = j
	}
	t.batchTouched = t.batchTouched[:0]
	if len(g) == 0 {
		t.foldScratch = g
		return
	}
	for level := 0; level < TwigHeight; level++ {
		// Parents of a sorted packed list are sorted; dedup is adjacent-only.
		// (Parents from different twigs can never collide: high bits differ.)
		w := 0
		last := ^uint64(0)
		for _, v := range g {
			p := (v &^ 0xfff) | ((v & 0xfff) >> 1)
			if p == last {
				continue
			}
			last = p
			g[w] = p
			w++
		}
		g = g[:w]
		t.hashPackedList(g)
	}
	// Each surviving element is (twigID, nodeIdx 1): publish the new roots.
	for _, v := range g {
		tw := t.twigs[v>>12]
		tw.root = tw.nodes[1]
	}
	t.foldScratch = g[:0]
}

// hashPackedList computes nodes[p] = hashNode(nodes[2p], nodes[2p+1]) for a
// sorted, deduplicated packed (twigID<<12 | p) list. Consecutive same-twig
// runs (>= 8) compress directly out of / into the twig heap; scattered
// parents — across twig boundaries — are gathered eight at a time for one
// SIMD pass each, which also overlaps their independent cache misses.
func (t *Tree) hashPackedList(g []uint64) {
	if !haveAVX2 {
		for _, v := range g {
			nodes, p := t.twigs[v>>12].nodes, v&0xfff
			nodes[p] = hashNode(nodes[2*p], nodes[2*p+1])
		}
		return
	}
	var pairs [16]Hash
	var out [8]Hash
	var pendN [8]*[2 * TwigSize]Hash
	var pendI [8]uint32
	np := 0
	flush := func() {
		if np == 8 {
			for m := 0; m < 8; m++ {
				pairs[2*m] = pendN[m][2*pendI[m]]
				pairs[2*m+1] = pendN[m][2*pendI[m]+1]
			}
			hashNodes8(&out, &pairs)
			for m := 0; m < 8; m++ {
				pendN[m][pendI[m]] = out[m]
			}
		} else {
			for m := 0; m < np; m++ {
				nodes, p := pendN[m], pendI[m]
				nodes[p] = hashNode(nodes[2*p], nodes[2*p+1])
			}
		}
		np = 0
	}
	for i := 0; i < len(g); {
		// Extend a consecutive run; +1 cannot cross a twig boundary unnoticed
		// (index 0xfff would carry into the twig bits), but check anyway.
		j := i + 1
		for j < len(g) && g[j] == g[j-1]+1 && g[j]>>12 == g[j-1]>>12 {
			j++
		}
		if j-i >= 8 {
			flush()
			tw := t.twigs[g[i]>>12]
			hashNodesRun(tw.nodes[:], int(g[i]&0xfff), j-i)
		} else {
			tw := t.twigs[g[i]>>12]
			for k := i; k < j; k++ {
				pendN[np] = tw.nodes
				pendI[np] = uint32(g[k] & 0xfff)
				np++
				if np == 8 {
					flush()
				}
			}
		}
		i = j
	}
	flush()
}
