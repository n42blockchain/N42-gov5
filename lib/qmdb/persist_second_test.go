// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"testing"
)

// nthFailPutter fails its n-th row (a failed capture).
type nthFailPutter struct {
	rowRec
	n int
}

var errInjectedPut = errors.New("injected put failure")

func (f *nthFailPutter) Put(table string, key, value []byte) error {
	if f.n--; f.n < 0 {
		return errInjectedPut
	}
	return f.rowRec.Put(table, key, value)
}

func (f *nthFailPutter) Delete(table string, key []byte) error {
	if f.n--; f.n < 0 {
		return errInjectedPut
	}
	return f.rowRec.Delete(table, key)
}

// secondRun drives two trees in lockstep through the lag writer's pipeline:
// block N's root runs while write N-1 is in flight (N-1's window staged),
// then
//
//	serial (today): commit N-1, evict N-1's window, FlushTo(N)
//	early  (r99a):  FlushToSecond(N), commit N-1, evict N-1's window
//
// with failed writes of N-1 after N's capture (both reload from their
// stores), failed captures of N, unwinds, compactions and proofs that
// rehydrate twigs. Every block's rows (order included), the store, the root
// and the twig residency after the join must be the same.
type secondRun struct {
	seed     int64
	blocks   int
	opsPer   int
	lagK     int
	retained uint64
}

type pipeTree struct {
	tr      *Tree
	st      mapStore
	lag     EvictLag
	flushed uint64  // adopted cursor
	staged  *rowRec // the in-flight write's rows (window staged)
	next    uint64  // its cursor
	undos   []*BlockUndo
}

func newPipeTree(lagK int) *pipeTree {
	p := &pipeTree{tr: New(), st: newMapStore(), lag: EvictLag{K: lagK}}
	p.attach()
	return p
}

func (p *pipeTree) attach() {
	p.tr.SetCold(ColdReaderFromGetter(p.st))
	p.tr.SetLeafStore(LeafStoreFromGetter(p.st))
}

func (p *pipeTree) evictThrough(retained uint64) uint64 { return sub(p.flushed, retained) }

func sub(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}

// commitStaged lands the in-flight write and adopts its cursor.
func (p *pipeTree) commitStaged(t *testing.T) {
	p.staged.applyTo(t, p.st)
	p.tr.CommitFlush()
	p.flushed = p.next
	p.staged = nil
}

func (p *pipeTree) reload(t *testing.T) {
	t.Helper()
	p.tr.SetIndex(newFlatIndex())
	if err := p.tr.LoadFrom(p.st); err != nil {
		t.Fatalf("reload: %v", err)
	}
	p.attach()
	p.lag.Reset()
	p.staged = nil
	p.undos = p.undos[:0]
	p.flushed = p.tr.NextSlot()
}

func treeDigest(tr *Tree, st mapStore, withStore bool) [32]byte {
	h := sha256.New()
	root := tr.Root()
	h.Write(root[:])
	for id, tw := range tr.twigs {
		if tw == nil {
			continue
		}
		var f byte
		if tw.nodes != nil {
			f |= 1
		}
		if tw.pruned {
			f |= 2
		}
		if tw.metaDirty {
			f |= 4
		}
		fmt.Fprintf(h, "%d:%d,", id, f)
	}
	fmt.Fprintf(h, "|%d|%d|%d|%v", tr.EntriesBase(), tr.ResidentEntries(), tr.NextSlot(), slices.Sorted(slices.Values(tr.deadFlushed)))
	if !withStore {
		var d [32]byte
		copy(d[:], h.Sum(nil))
		return d
	}
	tables := make([]string, 0, len(st))
	for tb := range st {
		tables = append(tables, tb)
	}
	slices.Sort(tables)
	for _, tb := range tables {
		keys := make([]string, 0, len(st[tb]))
		for k := range st[tb] {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s|%x|%x;", tb, k, st[tb][k])
		}
	}
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d
}

func (cfg secondRun) run(t *testing.T) (blocksDone, secondCaptures int) {
	t.Helper()
	rng := rand.New(rand.NewSource(cfg.seed))
	ser, ear := newPipeTree(cfg.lagK), newPipeTree(cfg.lagK)
	both := []*pipeTree{ser, ear}
	var keys []Hash
	live := map[Hash]bool{}
	key := func(i int) Hash {
		var h Hash
		binary.BigEndian.PutUint64(h[:8], uint64(i))
		h[8] = byte(cfg.seed)
		return sha256.Sum256(h[:])
	}
	nSame := 0
	same := func(what string) {
		t.Helper()
		nSame++
		full := nSame%10 == 0 || what == "end"
		if a, b := treeDigest(ser.tr, ser.st, full), treeDigest(ear.tr, ear.st, full); a != b {
			t.Fatalf("seed %d %s: serial and early trees differ", cfg.seed, what)
		}
	}
	resync := func() {
		live = map[Hash]bool{}
		for _, k := range keys {
			if _, ok := ser.tr.Get(k); ok {
				live[k] = true
			}
		}
	}
	drain := func() {
		// Join the in-flight write (no capture behind it).
		for _, p := range both {
			if p.staged != nil {
				p.commitStaged(t)
				p.lag.Evict(p.tr, p.evictThrough(cfg.retained), p.flushed)
			}
		}
	}
	for b := 0; b < cfg.blocks; b++ {
		if rng.Intn(4) == 0 && len(keys) > 0 {
			for i := 0; i < 1+rng.Intn(3); i++ {
				k := keys[rng.Intn(len(keys))]
				ser.tr.GetProof(k)
				ear.tr.GetProof(k)
			}
		}
		n := cfg.opsPer/2 + rng.Intn(cfg.opsPer)
		seen := map[Hash]bool{}
		var ops []Op
		for i := 0; i < n; i++ {
			var k Hash
			r := rng.Intn(10)
			if r < 5 || len(keys) == 0 {
				k = key(len(keys))
				keys = append(keys, k)
			} else {
				k = keys[rng.Intn(len(keys))]
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			if r == 9 && live[k] {
				ops = append(ops, Op{KeyHash: k})
				continue
			}
			v := make([]byte, 8+rng.Intn(40))
			rng.Read(v)
			ops = append(ops, Op{KeyHash: k, Value: v})
		}
		// Block b's root, with write b-1 in flight.
		var u *BlockUndo
		for _, p := range both {
			p.tr.StartUndoRecording()
			p.tr.ApplyOps(ops)
			u = p.tr.StopUndoRecording()
			p.undos = append(p.undos, u)
			p.tr.Root()
		}
		failPrev := ser.staged != nil && rng.Intn(15) == 0
		failCap := !failPrev && rng.Intn(23) == 0
		capAt := rng.Intn(64)

		// Early: capture b first.
		var recE Putter
		recEr := &rowRec{}
		recE = recEr
		if failCap {
			recE = &nthFailPutter{n: capAt}
		}
		var nextE uint64
		var emul []int
		var errE error
		if ear.staged != nil {
			nextE, _, emul, errE = ear.tr.FlushToSecond(recE, ear.next, sub(ear.next, cfg.retained))
			secondCaptures++
		} else {
			nextE, _, errE = ear.tr.FlushTo(recE, ear.flushed)
		}
		if failPrev {
			// Write b-1 failed: both reload from their stores (the early
			// capture of b is dropped with the aborted windows).
			ser.tr.AbortFlush()
			ear.tr.AbortFlush()
			ser.reload(t)
			ear.reload(t)
			resync()
			same(fmt.Sprintf("reload after failed write %d", b-1))
			continue
		}
		// Serial: join b-1 (commit + evict), then capture b.
		if ser.staged != nil {
			ser.commitStaged(t)
			ser.lag.Evict(ser.tr, ser.evictThrough(cfg.retained), ser.flushed)
		}
		var recS Putter
		recSr := &rowRec{}
		recS = recSr
		if failCap {
			recS = &nthFailPutter{n: capAt}
		}
		nextS, _, errS := ser.tr.FlushTo(recS, ser.flushed)
		// Early: join b-1 after the capture.
		if errE != nil {
			ear.tr.AbortFlushNewest()
		}
		if ear.staged != nil {
			ear.commitStaged(t)
			through := ear.evictThrough(cfg.retained)
			if errE == nil {
				if got := ear.tr.EvictCandidates(through); !slices.Equal(got, emul) {
					t.Fatalf("seed %d block %d: eviction drops %v, the capture assumed %v", cfg.seed, b, got, emul)
				}
			}
			ear.lag.Evict(ear.tr, through, ear.flushed)
		}
		if (errS != nil) != (errE != nil) {
			t.Fatalf("seed %d block %d: capture errors differ: %v vs %v", cfg.seed, b, errS, errE)
		}
		if errS != nil {
			// The write of b fails on its capture: both reload.
			ser.tr.AbortFlush()
			ear.tr.AbortFlush()
			ser.reload(t)
			ear.reload(t)
			resync()
			same(fmt.Sprintf("reload after failed capture %d", b))
			continue
		}
		if !slices.Equal(recSr.ops, recEr.ops) {
			t.Fatalf("seed %d block %d: rows differ: serial %d rows, early %d rows", cfg.seed, b, len(recSr.ops), len(recEr.ops))
		}
		if nextS != nextE {
			t.Fatalf("seed %d block %d: cursors differ %d vs %d", cfg.seed, b, nextS, nextE)
		}
		ser.staged, ser.next = recSr, nextS
		ear.staged, ear.next = recEr, nextE
		for _, op := range ops {
			live[op.KeyHash] = len(op.Value) != 0
		}
		same(fmt.Sprintf("block %d", b))
		blocksDone++

		switch r := rng.Intn(25); {
		case r == 0 && len(ser.undos) > 1:
			// Unwind the last block (after joining its write).
			drain()
			for _, p := range both {
				rrec := &rowRec{}
				f, err := p.tr.ApplyUndoWithStorage(rrec, p.undos[len(p.undos)-1], p.flushed)
				if err != nil {
					t.Fatalf("seed %d block %d unwind: %v", cfg.seed, b, err)
				}
				p.tr.CommitFlush()
				rrec.applyTo(t, p.st)
				p.flushed = f
				p.undos = p.undos[:len(p.undos)-1]
			}
			resync()
			same(fmt.Sprintf("unwind %d", b))
		case r == 1:
			drain()
			for _, p := range both {
				p.tr.StartUndoRecording()
				p.tr.Compact(0.3)
				p.tr.StopUndoRecording()
				p.undos = p.undos[:0]
			}
			resync()
			same(fmt.Sprintf("compact %d", b))
		}
	}
	drain()
	same("end")
	return blocksDone, secondCaptures
}

// TestFlushToSecondSameRows: the early capture (FlushToSecond before the
// previous window's commit and eviction) writes exactly the rows of the
// serial capture, and leaves the same tree and store, under the evict lag,
// a retained hot window, every twig-walk mode, failed writes, failed
// captures, unwinds and compactions.
func TestFlushToSecondSameRows(t *testing.T) {
	seeds := 6
	if testing.Short() {
		seeds = 2
	}
	for _, mode := range []string{"", "1", "check"} {
		for _, lagK := range []int{0, 4} {
			for _, retained := range []uint64{0, 3 * TwigSize / 2} {
				t.Run(fmt.Sprintf("set=%q/lag=%d/retained=%d", mode, lagK, retained), func(t *testing.T) {
					defer SetFlushTwigSetForTest(mode)()
					var done, second int
					for s := 0; s < seeds; s++ {
						d, sc := secondRun{seed: int64(100 + s), blocks: 80, opsPer: 900, lagK: lagK, retained: retained}.run(t)
						done += d
						second += sc
					}
					t.Logf("blocks %d, second-window captures %d", done, second)
					if second < seeds*40 {
						t.Fatalf("only %d second-window captures", second)
					}
				})
			}
		}
	}
}

// TestSecondWindowBookkeeping: CommitFlush promotes the second window,
// AbortFlush drops both (re-queueing both dead lists), AbortFlushNewest
// only the second, and a plain FlushTo is refused while two are staged.
func TestSecondWindowBookkeeping(t *testing.T) {
	st := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(st))
	tr.SetLeafStore(LeafStoreFromGetter(st))
	set := func(i int) Hash {
		var h Hash
		h[0], h[1] = byte(i), byte(i>>8)
		tr.Set(h, []byte{byte(i), 1})
		return h
	}
	var ks []Hash
	for i := 0; i < 50; i++ {
		ks = append(ks, set(i))
	}
	tr.Root()
	r0 := &rowRec{}
	n0, _, err := tr.FlushTo(r0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r0.applyTo(t, st)
	tr.CommitFlush()
	tr.EvictThrough(n0)
	// Window A: deaths of flushed slots.
	tr.Delete(ks[0])
	tr.Root()
	ra := &rowRec{}
	na, _, err := tr.FlushTo(ra, n0)
	if err != nil {
		t.Fatal(err)
	}
	// Window B while A is staged.
	tr.Delete(ks[1])
	tr.Root()
	if _, _, _, err := tr.FlushToSecond(&rowRec{}, na, n0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.FlushTo(&rowRec{}, na); !errors.Is(err, errSecondWindowStaged) {
		t.Fatalf("plain flush with two windows: %v", err)
	}
	if _, _, _, err := tr.FlushToSecond(&rowRec{}, na, n0); !errors.Is(err, errSecondWindowStaged) {
		t.Fatalf("third window: %v", err)
	}
	if len(tr.stagedDead) != 1 || len(tr.stagedNext.dead) != 1 {
		t.Fatalf("dead lists A %v B %v", tr.stagedDead, tr.stagedNext.dead)
	}
	// AbortFlushNewest: only B's deletes go back.
	tr.AbortFlushNewest()
	if tr.SecondWindowStaged() || !tr.stagedFlushValid || len(tr.deadFlushed) != 1 || len(tr.stagedDead) != 1 {
		t.Fatalf("after AbortFlushNewest: second %v first %v dead %v staged %v", tr.SecondWindowStaged(), tr.stagedFlushValid, tr.deadFlushed, tr.stagedDead)
	}
	// B again, then CommitFlush: A adopted, B promoted.
	if _, _, _, err := tr.FlushToSecond(&rowRec{}, na, n0); err != nil {
		t.Fatal(err)
	}
	tr.CommitFlush()
	if tr.flushedThrough != na || !tr.stagedFlushValid || tr.SecondWindowStaged() || len(tr.stagedDead) != 1 {
		t.Fatalf("after CommitFlush: flushed %d (want %d) first %v second %v staged %v", tr.flushedThrough, na, tr.stagedFlushValid, tr.SecondWindowStaged(), tr.stagedDead)
	}
	// AbortFlush with two windows re-queues both.
	tr.Delete(ks[2])
	tr.Root()
	if _, _, _, err := tr.FlushToSecond(&rowRec{}, tr.stagedFlushThrough, na); err != nil {
		t.Fatal(err)
	}
	tr.AbortFlush()
	if tr.stagedFlushValid || tr.SecondWindowStaged() || len(tr.deadFlushed) != 2 {
		t.Fatalf("after AbortFlush: first %v second %v dead %v", tr.stagedFlushValid, tr.SecondWindowStaged(), tr.deadFlushed)
	}
	// No first window: refused.
	if _, _, _, err := tr.FlushToSecond(&rowRec{}, na, na); err == nil {
		t.Fatal("a second window without a first was accepted")
	}
}
