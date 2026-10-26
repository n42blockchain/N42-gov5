// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package parallel

import (
	"math/bits"
	"os"
	"sync"
)

// scratchPool is N42_BLOCK_SCRATCH_POOL (default on; "0" allocates every
// block's scratch fresh, the old behaviour): an executor's store keeps its
// version lists in a versionArena reused across blocks.
var scratchPool = os.Getenv("N42_BLOCK_SCRATCH_POOL") != "0"

// SetScratchPoolForTest switches the scratch reuse and returns the restore.
func SetScratchPoolForTest(on bool) func() {
	prev := scratchPool
	scratchPool = on
	return func() { scratchPool = prev }
}

// The version lists of an executor's multi-version store (r31a).
//
// Every location a block writes gets a version list, and a credited
// account's list grows with every credit: 1.5 GB of lists sized for their
// first writer and 3.7 GB of appends per 20 s of a follower's import at
// ~900k transfers a second (writeOwnedHint, writeDelta), ~290 bytes per
// transaction. All of it dies with the block's store (Executor.Release).
// A versionArena carves the lists out of slabs in power-of-two classes,
// takes a list that grows back for the next request of its class, and is
// itself kept across blocks: the next block's store carves the same slabs
// again.
//
// Lifetime: the arena is released with its executor (Release), after the
// caller applied the store; it is cleared before it is kept, so it holds
// no value of the block. Nothing outside mvs.go holds a version list: the
// readers copy what they return out of it under the entry's lock (the
// values themselves live in the writers' slabs, not in the lists), and a
// list replaced by growth is unreachable once the entry's lock is
// released. Stale entries of a released store stay reachable only from
// the executor arena's kept read descriptors, which are cleared before
// their transaction runs again and never dereferenced before.

const (
	// versionClasses: lists of up to 1<<(versionClasses-1) versions come
	// from the arena; a longer one is allocated (a hot recipient of a whole
	// block's credits).
	versionClasses = 17
	// versionSlabLen is a shard's slab size in versions (40 KB).
	versionSlabLen = 512
)

// versionArena holds a store's version lists, one allocator per index
// shard (a list is carved under its shard's lock; the entry's lock is held
// around it, never the other way round).
type versionArena struct {
	shards [mvsShards]versionShard
}

type versionShard struct {
	mu    sync.Mutex
	free  [versionClasses][][]versionedValue
	slabs [][]versionedValue
	cur   int // slab being carved
	off   int // carved versions of slabs[cur]
	_     [32]byte
}

// versionClass is the class of a list of capacity n (n >= 1): the
// smallest c with 1<<c >= n.
func versionClass(n int) int {
	if n <= 1 {
		return 0
	}
	return bits.Len(uint(n - 1))
}

// alloc returns an empty list of capacity 1<<c (c < versionClasses).
func (s *versionShard) alloc(c int) []versionedValue {
	n := 1 << c
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.free[c]; len(f) > 0 {
		l := f[len(f)-1]
		s.free[c] = f[:len(f)-1]
		return l[:0]
	}
	for s.cur < len(s.slabs) {
		if sl := s.slabs[s.cur]; len(sl)-s.off >= n {
			l := sl[s.off : s.off : s.off+n]
			s.off += n
			return l
		}
		s.cur++
		s.off = 0
	}
	sl := make([]versionedValue, max(versionSlabLen, n))
	s.slabs = append(s.slabs, sl)
	s.cur, s.off = len(s.slabs)-1, n
	return sl[0:0:n]
}

// give takes back a list replaced by growth (its capacity a class's). The
// stale versions in it are overwritten before they are read again.
func (s *versionShard) give(l []versionedValue) {
	c := versionClass(cap(l))
	if c >= versionClasses || cap(l) != 1<<c {
		return
	}
	s.mu.Lock()
	s.free[c] = append(s.free[c], l[:0])
	s.mu.Unlock()
}

// reset clears every slab (the values the block's versions pointed at are
// released) and starts carving from the first again.
func (a *versionArena) reset() {
	for i := range a.shards {
		s := &a.shards[i]
		for _, sl := range s.slabs {
			clear(sl)
		}
		for c := range s.free {
			clear(s.free[c])
			s.free[c] = s.free[c][:0]
		}
		s.cur, s.off = 0, 0
	}
}

// versionArenaFree keeps released arenas: one for the import path, one for
// an ahead run or a leader's build.
var versionArenaFree struct {
	mu   sync.Mutex
	list []*versionArena
}

// Three: with N42_LAG_EARLY_SNAPSHOT a block's executor stays alive until
// its child's run ends, while the grandchild's run takes another.
const versionArenaFreeMax = 3

func takeVersionArena() *versionArena {
	if !scratchPool {
		return nil
	}
	versionArenaFree.mu.Lock()
	defer versionArenaFree.mu.Unlock()
	if n := len(versionArenaFree.list); n > 0 {
		a := versionArenaFree.list[n-1]
		versionArenaFree.list = versionArenaFree.list[:n-1]
		return a
	}
	return new(versionArena)
}

// giveVersionArena clears a and keeps it (off the caller's goroutine: a
// full block's slabs are tens of megabytes).
func giveVersionArena(a *versionArena) {
	if a == nil {
		return
	}
	go func() {
		a.reset()
		versionArenaFree.mu.Lock()
		defer versionArenaFree.mu.Unlock()
		if len(versionArenaFree.list) < versionArenaFreeMax {
			versionArenaFree.list = append(versionArenaFree.list, a)
		}
	}()
}

// growVersions makes room for one more version in e's list (e.mu held):
// in place when there is capacity; else a list of the next class from the
// store's arena (want sizes an empty list), or append's growth without an
// arena.
func (m *MVS) growVersions(e *mvEntry, want int) {
	n := len(e.versions)
	if n < cap(e.versions) {
		e.versions = e.versions[:n+1]
		return
	}
	if m == nil || m.va == nil {
		e.versions = append(e.versions, versionedValue{})
		return
	}
	if cap(e.versions) > 0 {
		want = 0 // a hint sizes only an empty list
	}
	need := max(want, 2*n, 1)
	c := versionClass(need)
	if c >= versionClasses {
		l := make([]versionedValue, n+1, need)
		copy(l, e.versions)
		m.va.shards[e.hash>>56].give(e.versions)
		e.versions = l
		return
	}
	sh := &m.va.shards[e.hash>>56]
	l := sh.alloc(c)[:n+1]
	copy(l, e.versions)
	if cap(e.versions) > 0 {
		sh.give(e.versions)
	}
	e.versions = l
}

// release hands the store's arena back for the next block's store; m
// must not be used afterwards.
func (m *MVS) release() {
	if m == nil || m.va == nil {
		return
	}
	a := m.va
	m.va = nil
	giveVersionArena(a)
}
