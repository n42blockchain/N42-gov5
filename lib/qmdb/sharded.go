// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Lock-free thread-sharded QMDB (the AlDBaran/Pleiades idea adapted to the
// twig forest): the key space is split by the TOP log2(S) BITS of the keyHash
// into S completely independent Trees — disjoint twig forests, cursors,
// indices — so a batch partitioned by shard applies on S goroutines with ZERO
// shared mutable state (no locks, no atomics on the hot path). The world root
// is a small fixed binary fold over the S shard roots (15 hashNode calls for
// S=16).
//
// Determinism: callers apply ops in a canonical order (the engine sorts by
// keyHash); partitioning preserves relative order within each shard, and shard
// roots are independent of inter-shard interleaving, so the sharded root is
// reproducible across nodes exactly like the single tree's.
//
// NOTE: a sharded root is NOT equal to a single Tree's root over the same
// history — opt in for new chains only.

package qmdb

import (
	"fmt"
	"math/bits"
	"sync"
)

// ShardedTree is a lock-free, thread-sharded QMDB forest.
type ShardedTree struct {
	shards []*Tree
	shift  uint // keyHash[0] >> shift selects the shard (top bits)
}

// Op is one batched state operation. Value nil/empty = delete.
type Op struct {
	KeyHash Hash
	Value   []byte
}

// NewSharded creates a sharded tree with S shards (power of two, 2..256).
func NewSharded(s int) (*ShardedTree, error) {
	if s < 2 || s > 256 || bits.OnesCount(uint(s)) != 1 {
		return nil, fmt.Errorf("qmdb: shard count %d must be a power of two in [2,256]", s)
	}
	t := &ShardedTree{
		shards: make([]*Tree, s),
		shift:  uint(8 - bits.TrailingZeros(uint(s))),
	}
	for i := range t.shards {
		t.shards[i] = New()
	}
	return t, nil
}

// Shards returns the shard count.
func (t *ShardedTree) Shards() int { return len(t.shards) }

// Shard returns the underlying tree owning keyHash (for advanced wiring:
// per-shard eviction, cold readers, indices).
func (t *ShardedTree) Shard(keyHash Hash) *Tree { return t.shards[t.shardOf(keyHash)] }

// ShardAt returns shard i.
func (t *ShardedTree) ShardAt(i int) *Tree { return t.shards[i] }

func (t *ShardedTree) shardOf(keyHash Hash) int { return int(keyHash[0] >> t.shift) }

// Set routes a single write to its shard (single-threaded convenience; the
// lock-free path is ApplyBatch).
func (t *ShardedTree) Set(keyHash Hash, value []byte) {
	t.shards[t.shardOf(keyHash)].Set(keyHash, value)
}

// Delete routes a single delete to its shard.
func (t *ShardedTree) Delete(keyHash Hash) {
	t.shards[t.shardOf(keyHash)].Delete(keyHash)
}

// Get reads through to the owning shard.
func (t *ShardedTree) Get(keyHash Hash) ([]byte, bool) {
	return t.shards[t.shardOf(keyHash)].Get(keyHash)
}

// LiveCount sums the live keys across shards.
func (t *ShardedTree) LiveCount() int {
	n := 0
	for _, s := range t.shards {
		n += s.LiveCount()
	}
	return n
}

// ApplyBatch partitions ops by shard (stable: relative order within a shard is
// preserved, so a canonically-ordered batch stays deterministic) and applies
// every shard's slice on its own goroutine — the lock-free hot path. Returns
// the new world root.
func (t *ShardedTree) ApplyBatch(ops []Op) Hash {
	parts := make([][]Op, len(t.shards))
	for i := range ops {
		s := t.shardOf(ops[i].KeyHash)
		parts[s] = append(parts[s], ops[i])
	}
	var wg sync.WaitGroup
	for s, part := range parts {
		if len(part) == 0 {
			continue
		}
		wg.Add(1)
		go func(tr *Tree, part []Op) {
			defer wg.Done()
			for i := range part {
				if len(part[i].Value) == 0 {
					tr.Delete(part[i].KeyHash)
				} else {
					tr.Set(part[i].KeyHash, part[i].Value)
				}
			}
			tr.Root() // fold this shard's dirty paths in parallel with the others
		}(t.shards[s], part)
	}
	wg.Wait()
	return t.Root()
}

// Root folds the shard roots into the world root. Shard Root() calls are
// cheap when ApplyBatch already folded them (rootDirty=false fast path).
func (t *ShardedTree) Root() Hash {
	roots := make([]Hash, len(t.shards))
	for i, s := range t.shards {
		roots[i] = s.Root()
	}
	for n := len(roots); n > 1; n /= 2 {
		for i := 0; i < n/2; i++ {
			roots[i] = hashNode(roots[2*i], roots[2*i+1])
		}
	}
	return roots[0]
}

// ShardedProof is a membership proof in a sharded forest: the owning shard's
// proof plus the top-tree path from the shard root to the world root.
type ShardedProof struct {
	Inner   Proof
	ShardID uint8
	TopPath []Hash // one sibling per top level, bottom-up
}

// GetProof produces a proof for a live key, or false if absent.
func (t *ShardedTree) GetProof(keyHash Hash) (*ShardedProof, bool) {
	sid := t.shardOf(keyHash)
	inner, ok := t.shards[sid].GetProof(keyHash)
	if !ok {
		return nil, false
	}
	// Top path: recompute the fold, collecting the sibling at each level.
	roots := make([]Hash, len(t.shards))
	for i, s := range t.shards {
		roots[i] = s.Root()
	}
	sp := &ShardedProof{Inner: *inner, ShardID: uint8(sid)}
	idx := sid
	for n := len(roots); n > 1; n /= 2 {
		sp.TopPath = append(sp.TopPath, roots[idx^1])
		for i := 0; i < n/2; i++ {
			roots[i] = hashNode(roots[2*i], roots[2*i+1])
		}
		idx /= 2
	}
	return sp, true
}

// VerifyShardedProof checks a sharded membership proof against a world root.
func VerifyShardedProof(root Hash, p *ShardedProof) bool {
	// Fold the inner proof to the shard root.
	shardRoot := foldProof(&p.Inner)
	idx := int(p.ShardID)
	node := shardRoot
	for _, sib := range p.TopPath {
		if idx&1 == 0 {
			node = hashNode(node, sib)
		} else {
			node = hashNode(sib, node)
		}
		idx /= 2
	}
	return node == root
}

// foldProof folds a single-tree proof to its (shard) root — the same math as
// VerifyProof without the final comparison.
func foldProof(p *Proof) Hash {
	node := hashLeaf(p.KeyHash, p.Value)
	idx := int(p.Slot % TwigSize)
	for L := 0; L < TwigHeight; L++ {
		if idx&1 == 0 {
			node = hashNode(node, p.TwigPath[L])
		} else {
			node = hashNode(p.TwigPath[L], node)
		}
		idx >>= 1
	}
	twigID := int(p.Slot / TwigSize)
	for L := 0; L < len(p.UpperPath); L++ {
		if twigID&1 == 0 {
			node = hashNode(node, p.UpperPath[L])
		} else {
			node = hashNode(p.UpperPath[L], node)
		}
		twigID >>= 1
	}
	return node
}

// shardTablePutter / shardTableGetter prefix persistence keys with the shard
// ID so all shards share one physical table set.
type shardTablePutter struct {
	inner Putter
	sid   byte
}

func (p shardTablePutter) Put(table string, k, v []byte) error {
	return p.inner.Put(table, append([]byte{p.sid}, k...), v)
}

// Delete passes the dead-row pruning capability through when the wrapped
// Putter has it (FlushTo type-asserts Deleter).
func (p shardTablePutter) Delete(table string, k []byte) error {
	if d, ok := p.inner.(Deleter); ok {
		return d.Delete(table, append([]byte{p.sid}, k...))
	}
	return nil
}

type shardTableGetter struct {
	inner Getter
	sid   byte
}

func (g shardTableGetter) GetOne(table string, k []byte) ([]byte, error) {
	return g.inner.GetOne(table, append([]byte{g.sid}, k...))
}

// FlushTo persists every shard through the same Putter, with per-shard key
// prefixes. flushedThrough bookkeeping is the caller's per shard (see
// ShardedFlushCursors).
func (t *ShardedTree) FlushTo(p Putter, flushedThrough []uint64) ([]uint64, int, error) {
	if len(flushedThrough) != len(t.shards) {
		flushedThrough = make([]uint64, len(t.shards))
	}
	next := make([]uint64, len(t.shards))
	total := 0
	for i, s := range t.shards {
		n, written, err := s.FlushTo(shardTablePutter{inner: p, sid: byte(i)}, flushedThrough[i])
		if err != nil {
			return nil, total, err
		}
		next[i] = n
		total += written
	}
	return next, total, nil
}

// LoadFrom rebuilds every shard from a Getter written by FlushTo.
func (t *ShardedTree) LoadFrom(g Getter) error {
	for i, s := range t.shards {
		if err := s.LoadFrom(shardTableGetter{inner: g, sid: byte(i)}); err != nil {
			return fmt.Errorf("shard %d: %w", i, err)
		}
	}
	return nil
}
