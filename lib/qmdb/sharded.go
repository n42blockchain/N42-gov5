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
	"runtime"
	"sync/atomic"
)

// ShardedTree is a lock-free, thread-sharded QMDB forest.
type ShardedTree struct {
	shards []*Tree
	shift  uint // keyHash[0] >> shift selects the shard (top bits)

	// ApplyBatch partition scratch, reused across blocks (callers serialize
	// ApplyBatch like every other mutator, so reuse is safe).
	partCounts []int
	partBuf    []Op
	parts      [][]Op

	pool *shardPool // lazily started persistent workers (see below)
}

// shardPool keeps ApplyBatch's workers alive across blocks. Spawning (or
// waking parked) goroutines per block cost 5-25µs on Windows — comparable to
// the whole parallel section once per-shard work shrank to tens of ops — so
// workers SPIN briefly on the epoch counter before parking on a channel:
// back-to-back blocks (the chain-import steady state) hit the spin window and
// dispatch in nanoseconds. Publication order: parts/next/done are written
// before the epoch bump (atomic = release); workers re-read them only after
// observing the new epoch.
type shardPool struct {
	epoch  atomic.Uint64
	next   atomic.Int64
	done   atomic.Int64
	parts  [][]Op
	shards []*Tree
	wake   []chan struct{}
	nw     int
}

const poolSpins = 1 << 13

func newShardPool(shards []*Tree) *shardPool {
	nw := runtime.GOMAXPROCS(0) - 1 // the ApplyBatch caller is worker #nw
	if nw > len(shards)-1 {
		nw = len(shards) - 1
	}
	if nw < 0 {
		nw = 0
	}
	p := &shardPool{shards: shards, wake: make([]chan struct{}, nw), nw: nw}
	for w := 0; w < nw; w++ {
		p.wake[w] = make(chan struct{}, 1)
		go p.run(p.wake[w])
	}
	return p
}

func (p *shardPool) run(wake <-chan struct{}) {
	last := uint64(0)
	for {
		spins := 0
		for p.epoch.Load() == last {
			spins++
			if spins >= poolSpins {
				<-wake // park; dispatcher wakes every worker individually
				spins = 0
			}
		}
		last = p.epoch.Load()
		p.work()
		p.done.Add(1)
	}
}

func (p *shardPool) work() {
	for {
		s := int(p.next.Add(1)) - 1
		if s >= len(p.parts) {
			return
		}
		if len(p.parts[s]) == 0 {
			continue
		}
		p.shards[s].ApplyOps(p.parts[s])
	}
}

// dispatch runs one block's shard parts across the pool plus the caller, and
// returns when every shard is applied.
func (p *shardPool) dispatch(parts [][]Op) {
	p.parts = parts
	p.next.Store(0)
	p.done.Store(0)
	p.epoch.Add(1)
	// Wake every worker through its own one-slot mailbox. A shared buffered
	// channel is not a broadcast: one fast worker can consume several tokens
	// while the others remain parked, leaving the done barrier short forever.
	// Per-worker mailboxes preserve the spin fast path without lost wakeups.
	for _, wake := range p.wake {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	p.work() // caller participates instead of idling at the barrier
	for spins := 1; p.done.Load() != int64(p.nw); spins++ {
		if spins%poolSpins == 0 {
			runtime.Gosched() // a straggler may need our core
		}
	}
	p.parts = nil
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
	// Counting scatter into one backing array — no per-append reallocation,
	// scratch reused across blocks, and the partition pass touches each op
	// exactly twice.
	if t.partCounts == nil {
		t.partCounts = make([]int, len(t.shards))
		t.parts = make([][]Op, len(t.shards))
	}
	counts := t.partCounts
	clear(counts)
	for i := range ops {
		counts[t.shardOf(ops[i].KeyHash)]++
	}
	if cap(t.partBuf) < len(ops) {
		t.partBuf = make([]Op, len(ops))
	}
	buf := t.partBuf[:len(ops)]
	parts := t.parts
	off := 0
	for s, c := range counts {
		parts[s] = buf[off : off : off+c]
		off += c
	}
	for i := range ops {
		s := t.shardOf(ops[i].KeyHash)
		parts[s] = append(parts[s], ops[i])
	}
	// Persistent spin-then-park workers claim shards off an atomic counter —
	// per-block goroutine spawn/wakeup latency was comparable to the whole
	// parallel section once per-shard work shrank to tens of ops.
	if t.pool == nil {
		t.pool = newShardPool(t.shards)
	}
	t.pool.dispatch(parts)
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
	local := p.Slot % TwigSize
	if p.Bits[local/8]&(1<<uint(local%8)) == 0 {
		return Hash{} // slot not live in the presented bitmap
	}
	node := hashLeaf(p.KeyHash, p.Value)
	idx := int(local)
	for L := 0; L < TwigHeight; L++ {
		if idx&1 == 0 {
			node = hashNode(node, p.TwigPath[L])
		} else {
			node = hashNode(p.TwigPath[L], node)
		}
		idx >>= 1
	}
	bits := p.Bits
	node = hashNode(node, hashBits(&bits))
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

// CommitFlush finalizes the last FlushTo on every shard after the surrounding
// tx committed (see Tree.CommitFlush).
func (t *ShardedTree) CommitFlush() {
	for _, s := range t.shards {
		s.CommitFlush()
	}
}

// AbortFlush re-queues every shard's staged dead-row reclaims after the
// surrounding tx rolled back (see Tree.AbortFlush).
func (t *ShardedTree) AbortFlush() {
	for _, s := range t.shards {
		s.AbortFlush()
	}
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
