// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the incremental speculative reload (LoadIncremental): it must
// reproduce a full rebuild EXACTLY while reading only what changed, and it must
// reject — never paper over — anything its delta scan cannot account for.

package qmdb

import (
	"errors"
	"fmt"
	"testing"
)

// countingStore counts per-table point reads so a test can assert that a reload
// really is proportional to the delta and not to the chain's history.
type countingStore struct {
	mapStore
	reads map[string]int
}

func newCountingStore() *countingStore {
	return &countingStore{mapStore: newMapStore(), reads: map[string]int{}}
}

func (c *countingStore) GetOne(table string, key []byte) ([]byte, error) {
	c.reads[table]++
	return c.mapStore.GetOne(table, key)
}

func (c *countingStore) resetCounts() { c.reads = map[string]int{} }

// incFreshOps returns n fresh inserts for block b (bulk history growth).
func incFreshOps(b, n uint64) []rvOp {
	ops := make([]rvOp, 0, n)
	for j := uint64(0); j < n; j++ {
		k := b*1_000_000 + j
		ops = append(ops, rvOp{key: rvKey(k), val: rvVal("v", k)})
	}
	return ops
}

// incWindowOps is the live block shape a leader sees between two builds: a few
// fresh keys plus overwrites of long-lived hot keys, whose current slots sit deep
// in SEALED history — exactly the "an old twig's live bits change" case.
func incWindowOps(b uint64) []rvOp {
	ops := []rvOp{
		{key: rvKey(7_000_000 + b), val: rvVal("fresh", b)},
		{key: rvKey(1_000_000 + 3), val: rvVal("hot", b)},
		{key: rvKey(1_000_000 + 11), val: rvVal("hot", b)},
		{key: rvKey(2_000_000 + 7), val: rvVal("hot", b)},
	}
	return ops
}

// incChain is a live chain writing through a store, block by block, exactly like
// the canonical computer does (flush + evict entries + evict twig leaves).
type incChain struct {
	tree    *Tree
	store   *countingStore
	flushed uint64
}

func newIncChain(t *testing.T, baseBlocks, opsPerBlock uint64) *incChain {
	t.Helper()
	st := newCountingStore()
	c := &incChain{tree: New(), store: st}
	c.tree.SetCold(ColdReaderFromGetter(st))
	c.tree.SetLeafStore(LeafStoreFromGetter(st))
	for b := uint64(1); b <= baseBlocks; b++ {
		c.block(t, incFreshOps(b, opsPerBlock))
	}
	return c
}

func (c *incChain) block(t *testing.T, ops []rvOp) {
	t.Helper()
	rvApply(c.tree, ops)
	c.tree.Root()
	var err error
	if c.flushed, _, err = c.tree.FlushTo(c.store, c.flushed); err != nil {
		t.Fatalf("flush: %v", err)
	}
	c.tree.CommitFlush()
	c.tree.EvictThrough(c.flushed)
	c.tree.EvictTwigsThrough(c.flushed)
}

// newMiner returns a fully loaded speculative computer over the same store,
// plus the load cursor and the fossil baseline the reloads are measured against.
func (c *incChain) newMiner(t *testing.T) (*Tree, uint64, int) {
	t.Helper()
	m := New()
	m.SetCold(ColdReaderFromGetter(c.store))
	m.SetLeafStore(LeafStoreFromGetter(c.store))
	if err := m.LoadFrom(c.store); err != nil {
		t.Fatalf("miner full load: %v", err)
	}
	return m, m.NextSlot(), m.LiveBits() - m.LiveCount()
}

// requireMatchesLive asserts the reloaded tree is indistinguishable from the
// canonical one AND from an independent from-scratch rebuild of the store.
func requireMatchesLive(t *testing.T, tag string, got *Tree, c *incChain) {
	t.Helper()
	if a, b := got.Root(), c.tree.Root(); a != b {
		t.Fatalf("%s: root %x != live %x", tag, a, b)
	}
	if a, b := got.LiveCount(), c.tree.LiveCount(); a != b {
		t.Fatalf("%s: index size %d != live %d", tag, a, b)
	}
	if a, b := got.LiveBits(), c.tree.LiveBits(); a != b {
		t.Fatalf("%s: live bits %d != live %d", tag, a, b)
	}
	if a, b := got.NextSlot(), c.tree.NextSlot(); a != b {
		t.Fatalf("%s: cursor %d != live %d", tag, a, b)
	}
	if a, b := got.NumTwigs(), c.tree.NumTwigs(); a != b {
		t.Fatalf("%s: twig count %d != live %d", tag, a, b)
	}
	ref := New()
	ref.SetCold(ColdReaderFromGetter(c.store))
	ref.SetLeafStore(LeafStoreFromGetter(c.store))
	if err := ref.LoadFrom(c.store); err != nil {
		t.Fatalf("%s: reference rebuild: %v", tag, err)
	}
	if a, b := got.Root(), ref.Root(); a != b {
		t.Fatalf("%s: root %x != independent full rebuild %x", tag, a, b)
	}
	if a, b := got.LiveCount(), ref.LiveCount(); a != b {
		t.Fatalf("%s: index size %d != independent full rebuild %d", tag, a, b)
	}
	auditIndex(t, got, tag)
}

// TestIncrementalReloadMatchesFullRebuild: the plain case — the chain advances
// with fresh keys plus overwrites of long-lived keys sitting in sealed twigs, and
// the incremental reload must land on the live root exactly.
func TestIncrementalReloadMatchesFullRebuild(t *testing.T) {
	c := newIncChain(t, 12, 900) // ~10800 appends => 6 twigs
	if c.tree.NumTwigs() < 4 {
		t.Fatalf("test needs a multi-twig forest, got %d", c.tree.NumTwigs())
	}
	miner, cursor, delta := c.newMiner(t)

	for b := uint64(100); b < 103; b++ {
		c.block(t, incWindowOps(b))
	}
	if err := miner.LoadIncremental(c.store, cursor, delta); err != nil {
		t.Fatalf("incremental reload: %v", err)
	}
	requireMatchesLive(t, "incremental", miner, c)
}

// TestIncrementalReloadReadsOnlyTheDelta: the point of the change. A full reload
// touches every twig's metadata; the incremental one must touch only the grown
// tail plus the sealed twigs whose live bits the new appends cleared.
func TestIncrementalReloadReadsOnlyTheDelta(t *testing.T) {
	c := newIncChain(t, 20, 900) // ~18000 appends => ~9 twigs
	miner, cursor, delta := c.newMiner(t)
	twigs := miner.NumTwigs()
	if twigs < 8 {
		t.Fatalf("test needs a deep forest, got %d twigs", twigs)
	}
	for b := uint64(100); b < 102; b++ {
		c.block(t, incWindowOps(b))
	}

	c.store.resetCounts()
	if err := miner.LoadIncremental(c.store, cursor, delta); err != nil {
		t.Fatalf("incremental reload: %v", err)
	}
	incrTwigReads := c.store.reads[TwigTable]

	// Same store, same delta, the pre-existing full-forest path for comparison.
	ref, refCursor, refDelta := c.newMiner(t)
	_ = refCursor
	_ = refDelta
	c.store.resetCounts()
	if err := ref.LoadFromTrustedIndex(c.store, ref.NextSlot(), ref.LiveBits()-ref.LiveCount()); err != nil {
		t.Fatalf("meta-scan reload: %v", err)
	}
	fullTwigReads := c.store.reads[TwigTable]

	if fullTwigReads < twigs {
		t.Fatalf("sanity: meta-scan reload read %d twig rows, expected >= %d", fullTwigReads, twigs)
	}
	// The window overwrites 3 hot keys per block; even if each lands in its own
	// sealed twig the incremental reload must stay far below the whole forest.
	if incrTwigReads*2 >= fullTwigReads {
		t.Fatalf("incremental reload read %d twig rows vs %d for the full forest — not proportional to the delta",
			incrTwigReads, fullTwigReads)
	}
	t.Logf("twig metadata reads: incremental=%d full=%d (forest=%d twigs)", incrTwigReads, fullTwigReads, twigs)
}

// TestIncrementalReloadBuildPeelRounds replays the miner's actual loop over many
// rounds: reload, build a speculative candidate, peel it, reload again — while
// the canonical chain keeps advancing underneath. Every round must reproduce the
// live root.
func TestIncrementalReloadBuildPeelRounds(t *testing.T) {
	c := newIncChain(t, 10, 900)
	miner, cursor, delta := c.newMiner(t)

	for round := uint64(0); round < 25; round++ {
		// The canonical chain seals a block (sometimes several).
		for k := uint64(0); k <= round%3; k++ {
			c.block(t, incWindowOps(1000+round*10+k))
		}
		if err := miner.LoadIncremental(c.store, cursor, delta); err != nil {
			t.Fatalf("round %d incremental reload: %v", round, err)
		}
		requireMatchesLive(t, fmt.Sprintf("round %d", round), miner, c)
		cursor = miner.NextSlot()
		delta = miner.LiveBits() - miner.LiveCount()

		// Speculative candidate on top, then peeled (never committed).
		undo := applyBlockRecorded(miner, incWindowOps(500_000+round))
		miner.Root()
		if err := miner.ApplyUndo(undo); err != nil {
			t.Fatalf("round %d candidate peel: %v", round, err)
		}
	}
}

// TestIncrementalReloadCrossesTwigBoundaries: the window must be able to fill the
// previously-active twig and open new ones — the previously-active twig's frozen
// leaf tree grows, so it may never be reused as-is.
func TestIncrementalReloadCrossesTwigBoundaries(t *testing.T) {
	c := newIncChain(t, 6, 900)
	miner, cursor, delta := c.newMiner(t)
	before := miner.NumTwigs()

	// Enough appends to add several twigs in one window.
	for b := uint64(200); b < 206; b++ {
		c.block(t, incFreshOps(b, 900))
	}
	if c.tree.NumTwigs() <= before+1 {
		t.Fatalf("window did not add twigs: %d -> %d", before, c.tree.NumTwigs())
	}
	if err := miner.LoadIncremental(c.store, cursor, delta); err != nil {
		t.Fatalf("incremental reload across new twigs: %v", err)
	}
	requireMatchesLive(t, "across new twigs", miner, c)
}

// TestIncrementalReloadRejectsInWindowDelete: a DELETE appends nothing, so the
// delta scan cannot see it — the persisted-root check (or the index
// reconciliation riding behind it) must reject the reload rather than ship a
// forest with a stale live bit. The caller's full-forest fallback then produces
// the right tree.
func TestIncrementalReloadRejectsInWindowDelete(t *testing.T) {
	c := newIncChain(t, 10, 900)
	miner, cursor, delta := c.newMiner(t)

	// Delete a key whose live slot sits deep in sealed history.
	victim := rvKey(1_000_000 + 3)
	if _, ok := c.tree.Get(victim); !ok {
		t.Fatal("test key is not live")
	}
	c.block(t, []rvOp{{key: victim, val: nil}})

	err := miner.LoadIncremental(c.store, cursor, delta)
	if err == nil {
		t.Fatal("in-window delete silently accepted by the incremental reload")
	}
	if errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("expected a rejection, got a decline: %v", err)
	}
	t.Logf("rejected as expected: %v", err)

	// Fallback chain, exactly as ReloadForBuild drives it.
	if err := miner.LoadFromTrustedIndex(c.store, cursor, delta); err == nil {
		requireMatchesLive(t, "meta-scan fallback", miner, c)
	} else {
		miner.SetIndex(NewMapIndex())
		if err := miner.LoadFrom(c.store); err != nil {
			t.Fatalf("full rebuild fallback: %v", err)
		}
		requireMatchesLive(t, "full rebuild fallback", miner, c)
	}
}

// TestIncrementalReloadRejectsReclaimedPredecessor covers the one gap the delta
// scan provably cannot close by itself, and proves the ROOT CHECK is what closes
// it (the index reconciliation cannot see this one at all).
//
// Shape: a key that is live in a sealed twig is OVERWRITTEN inside the window and
// then DELETED inside the same window. The overwrite's slot dies after it was
// flushed, so its entry row is reclaimed — the delta scan finds no row there and
// therefore cannot learn which sealed slot that append killed. Both the index
// population and the live-bit population stay wrong by exactly one, so their
// difference is unchanged and reconciliation passes; only the persisted world
// root disagrees.
func TestIncrementalReloadRejectsReclaimedPredecessor(t *testing.T) {
	c := newIncChain(t, 10, 900)
	miner, cursor, delta := c.newMiner(t)

	key := rvKey(1_000_003) // live since block 1 => deep in a sealed twig
	old, ok := miner.idx.Get(key)
	if !ok || old >= cursor {
		t.Fatalf("test key is not live in the sealed zone (slot=%d cursor=%d)", old, cursor)
	}
	c.block(t, []rvOp{{key: key, val: rvVal("overwrite", 1)}}) // kills the sealed slot
	c.block(t, []rvOp{{key: key, val: nil}})                   // kills the new slot; its row is reclaimed

	// Precondition of the scenario: the overwrite's row really is gone.
	newSlot := cursor
	if row, err := c.store.GetOne(EntryTable, be8(newSlot)); err != nil || len(row) != 0 {
		t.Fatalf("scenario setup: slot %d's row was not reclaimed (len=%d err=%v)", newSlot, len(row), err)
	}

	err := miner.LoadIncremental(c.store, cursor, delta)
	if !errors.Is(err, ErrIncrementalRootMismatch) {
		t.Fatalf("unattributable sealed-twig bit change not caught by the root check: %v", err)
	}
}

// TestIncrementalReloadDeclinesOnPreconditions: every "I cannot prove this" case
// must decline with ErrIncrementalUnavailable (a cheap fallback), never mutate
// the tree into something wrong.
func TestIncrementalReloadDeclinesOnPreconditions(t *testing.T) {
	c := newIncChain(t, 6, 900)
	miner, cursor, delta := c.newMiner(t)

	// Cursor mismatch: the previous candidate was not peeled.
	rvApply(miner, incWindowOps(1))
	miner.Root()
	if err := miner.LoadIncremental(c.store, cursor, delta); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("unpeeled candidate not declined: %v", err)
	}

	// No previous load cursor (first use).
	fresh := New()
	fresh.SetCold(ColdReaderFromGetter(c.store))
	if err := fresh.LoadIncremental(c.store, 0, 0); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("first use not declined: %v", err)
	}

	// Store rewound below the loaded cursor (a revert happened).
	m2, cur2, d2 := c.newMiner(t)
	if err := c.store.Put(MetaTable, []byte("nextSlot"), be8(cur2-10)); err != nil {
		t.Fatal(err)
	}
	if err := m2.LoadIncremental(c.store, cur2, d2); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("rewound store not declined: %v", err)
	}
	if err := c.store.Put(MetaTable, []byte("nextSlot"), be8(cur2)); err != nil {
		t.Fatal(err)
	}

	// No persisted world root => nothing to verify against => decline.
	m3, cur3, d3 := c.newMiner(t)
	if err := c.store.Delete(MetaTable, []byte("root")); err != nil {
		t.Fatal(err)
	}
	if err := m3.LoadIncremental(c.store, cur3, d3); !errors.Is(err, ErrIncrementalUnavailable) {
		t.Fatalf("missing persisted root not declined: %v", err)
	}
}

// BenchmarkReloadForBuild contrasts the three reload tiers on the same store, so
// the cost model ("full forest" vs "the delta") is measurable rather than argued.
func BenchmarkReloadForBuild(b *testing.B) {
	st := newCountingStore()
	live := New()
	live.SetCold(ColdReaderFromGetter(st))
	live.SetLeafStore(LeafStoreFromGetter(st))
	flushed := uint64(0)
	var err error
	for blk := uint64(1); blk <= 60; blk++ { // ~54k appends => ~27 twigs
		rvApply(live, incFreshOps(blk, 900))
		live.Root()
		if flushed, _, err = live.FlushTo(st, flushed); err != nil {
			b.Fatal(err)
		}
		live.CommitFlush()
		live.EvictThrough(flushed)
		live.EvictTwigsThrough(flushed)
	}
	load := func() (*Tree, uint64, int) {
		m := New()
		m.SetCold(ColdReaderFromGetter(st))
		m.SetLeafStore(LeafStoreFromGetter(st))
		if e := m.LoadFrom(st); e != nil {
			b.Fatal(e)
		}
		return m, m.NextSlot(), m.LiveBits() - m.LiveCount()
	}
	b.Run("incremental", func(b *testing.B) {
		m, cursor, delta := load()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if e := m.LoadIncremental(st, cursor, delta); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("meta-scan", func(b *testing.B) {
		m, cursor, delta := load()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if e := m.LoadFromTrustedIndex(st, cursor, delta); e != nil {
				b.Fatal(e)
			}
		}
	})
}
