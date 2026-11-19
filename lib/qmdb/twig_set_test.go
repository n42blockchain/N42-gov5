// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// rowRec records every row a flush or revert issues, in order.
type rowRec struct {
	ops []string
}

func (r *rowRec) Put(table string, key, value []byte) error {
	r.ops = append(r.ops, fmt.Sprintf("P|%s|%x|%x", table, key, value))
	return nil
}

func (r *rowRec) Delete(table string, key []byte) error {
	r.ops = append(r.ops, fmt.Sprintf("D|%s|%x", table, key))
	return nil
}

// applyTo replays the recorded rows into the store (the commit).
func (r *rowRec) applyTo(t *testing.T, st mapStore) {
	t.Helper()
	for _, op := range r.ops {
		var kind, table, k, v string
		parts := bytes.Split([]byte(op), []byte("|"))
		kind, table, k = string(parts[0]), string(parts[1]), string(parts[2])
		key := mustHex(t, k)
		if kind == "P" {
			v = string(parts[3])
			if err := st.Put(table, key, mustHex(t, v)); err != nil {
				t.Fatal(err)
			}
		} else if err := st.Delete(table, key); err != nil {
			t.Fatal(err)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

// twigSetRun drives one tree through a seeded workload the way the node's
// lag writer does (record undo, apply, capture = evict the adopted window +
// FlushTo into a buffer, commit or abort, unwind, reload) and returns one
// digest per step of everything observable: the rows (order included), the
// root, the resident/pruned/meta-dirty twig sets and the entry window.
type twigSetRun struct {
	seed     int64
	blocks   int
	opsPer   int
	lagK     int
	retained uint64
	shard    bool
}

type twigSetStep struct {
	what   string
	digest [32]byte
	rows   int
}

func (cfg twigSetRun) run(t *testing.T, mode string) (steps []twigSetStep, checked int) {
	t.Helper()
	defer SetFlushTwigSetForTest(mode)()
	if cfg.shard {
		prev := ShardApplyWorkers
		ShardApplyWorkers = 4
		defer func() { ShardApplyWorkers = prev }()
	}
	rng := rand.New(rand.NewSource(cfg.seed))
	st := newMapStore()
	tr := New()
	attach := func(x *Tree) {
		x.SetCold(ColdReaderFromGetter(st))
		x.SetLeafStore(LeafStoreFromGetter(st))
	}
	attach(tr)
	lag := EvictLag{K: cfg.lagK}
	var (
		flushed  uint64
		adopted  bool // a committed flush whose window was not yet evicted
		undos    []*BlockUndo
		keys     []Hash
		live     = map[Hash]bool{}
		side     *Tree
		sidePrev uint64
	)
	key := func(i int) Hash {
		var h Hash
		binary.BigEndian.PutUint64(h[:8], uint64(i))
		h[8] = byte(cfg.seed)
		return sha256.Sum256(h[:])
	}
	record := func(what string, rows []string) {
		h := sha256.New()
		for _, r := range rows {
			h.Write([]byte(r))
			h.Write([]byte{0})
		}
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
			if tw.dirty {
				f |= 8
			}
			fmt.Fprintf(h, "%d:%d,", id, f)
		}
		fmt.Fprintf(h, "|%d|%d|%d|%d", tr.EntriesBase(), tr.ResidentEntries(), tr.NextSlot(), flushed)
		var d [32]byte
		copy(d[:], h.Sum(nil))
		steps = append(steps, twigSetStep{what: what, digest: d, rows: len(rows)})
		if mode != "" {
			checkTwigSetSuperset(t, tr, what)
		}
	}
	evictAdopted := func() {
		if !adopted {
			return
		}
		through := flushed
		if through > cfg.retained {
			through -= cfg.retained
		} else {
			through = 0
		}
		lag.Evict(tr, through, flushed)
		adopted = false
		if mode == "check" && tr.LastEvictStats().SetMode == TwigWalkCheck {
			checked++
		}
	}
	reload := func() {
		tr.SetIndex(newFlatIndex())
		if err := tr.LoadFrom(st); err != nil {
			t.Fatalf("reload: %v", err)
		}
		attach(tr)
		lag.Reset()
		adopted = false
		live = map[Hash]bool{}
		for _, k := range keys {
			if _, ok := tr.Get(k); ok {
				live[k] = true
			}
		}
	}
	for b := 0; b < cfg.blocks; b++ {
		// Rehydrate a few evicted twigs the way a proof does.
		if rng.Intn(4) == 0 && len(keys) > 0 {
			for i := 0; i < 1+rng.Intn(3); i++ {
				tr.GetProof(keys[rng.Intn(len(keys))])
			}
		}
		// Build the block: inserts, updates of old (often evicted) keys,
		// deletes.
		n := cfg.opsPer/2 + rng.Intn(cfg.opsPer)
		seen := map[Hash]bool{}
		var ops []Op
		for i := 0; i < n; i++ {
			var k Hash
			r := rng.Intn(10)
			switch {
			case r < 5 || len(keys) == 0:
				k = key(len(keys))
				keys = append(keys, k)
			default:
				k = keys[rng.Intn(len(keys))]
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			// No deletes while a side tree waits for its incremental
			// reload (an in-window delete forces the full fallback).
			if r == 9 && live[k] && side == nil {
				ops = append(ops, Op{KeyHash: k})
				continue
			}
			v := make([]byte, 8+rng.Intn(40))
			rng.Read(v)
			ops = append(ops, Op{KeyHash: k, Value: v})
		}
		if cfg.shard {
			sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
		}
		tr.StartUndoRecording()
		if rng.Intn(5) == 0 {
			for _, op := range ops { // the unbatched path
				if len(op.Value) == 0 {
					tr.Delete(op.KeyHash)
				} else {
					tr.Set(op.KeyHash, op.Value)
				}
			}
		} else {
			tr.ApplyOps(ops)
		}
		u := tr.StopUndoRecording()
		tr.Root()
		// The capture: evict the adopted window, then flush this block.
		evictAdopted()
		rec := &rowRec{}
		next, _, err := tr.FlushTo(rec, flushed)
		if err != nil {
			t.Fatalf("block %d flush: %v", b, err)
		}
		if mode == "check" && tr.LastFlushStats().SetMode == TwigWalkCheck {
			checked++
		}
		if rng.Intn(17) == 0 {
			// Failed write: the rows never land; the tree is reloaded from
			// the store (recoverLagWrite).
			tr.AbortFlush()
			record(fmt.Sprintf("abort %d", b), rec.ops)
			reload()
			record(fmt.Sprintf("reload %d", b), nil)
			continue
		}
		rec.applyTo(t, st)
		tr.CommitFlush()
		flushed = next
		adopted = true
		undos = append(undos, u)
		for _, op := range ops {
			live[op.KeyHash] = len(op.Value) != 0
		}
		record(fmt.Sprintf("block %d", b), rec.ops)

		switch r := rng.Intn(20); {
		case r == 0 && len(undos) > 1:
			// Unwind one or two blocks, each in its own committed revert.
			for d := 0; d < 1+rng.Intn(2) && len(undos) > 0; d++ {
				evictAdopted()
				rrec := &rowRec{}
				f, err := tr.ApplyUndoWithStorage(rrec, undos[len(undos)-1], flushed)
				if err != nil {
					t.Fatalf("block %d unwind: %v", b, err)
				}
				tr.CommitFlush()
				rrec.applyTo(t, st)
				flushed = f
				undos = undos[:len(undos)-1]
				record(fmt.Sprintf("unwind %d.%d", b, d), rrec.ops)
			}
			side = nil // the store rewound below the side tree's cursor
			// Keys revived/removed by the unwind: resync the liveness map.
			live = map[Hash]bool{}
			for _, k := range keys {
				if _, ok := tr.Get(k); ok {
					live[k] = true
				}
			}
		case r == 1:
			reload()
			undos = undos[:0]
			record(fmt.Sprintf("reload %d", b), nil)
		case r == 2 && side == nil:
			// A second tree loaded at this cursor, flushed once (so its
			// set is valid), to be advanced incrementally later.
			side = New()
			side.SetCold(ColdReaderFromGetter(st))
			side.SetLeafStore(LeafStoreFromGetter(st))
			if err := side.LoadFrom(st); err != nil {
				t.Fatalf("side load: %v", err)
			}
			if _, _, err := side.FlushTo(&rowRec{}, side.NextSlot()); err != nil {
				t.Fatal(err)
			}
			side.AbortFlush()
			sidePrev = side.NextSlot()
		case r == 3 && side != nil:
			// The incremental reload, then the side tree takes over.
			evictAdopted()
			how := "incremental"
			if err := side.LoadIncremental(st, sidePrev, 0); err != nil {
				// An in-window delete defeats the trusted index (as in the
				// node): fall back to a full reload of the same tree.
				how = "incremental-fallback"
				side.SetIndex(newFlatIndex())
				if err := side.LoadFrom(st); err != nil {
					t.Fatalf("block %d incremental fallback: %v", b, err)
				}
			}
			tr, side = side, nil
			attach(tr)
			lag.Reset()
			undos = undos[:0]
			record(fmt.Sprintf("%s %d", how, b), nil)
		case r == 4:
			// Prune fully dead twigs (pruned twigs flush meta every time).
			tr.StartUndoRecording()
			tr.Compact(0.3)
			tr.StopUndoRecording()
			undos = undos[:0] // no unwind across a compaction in this harness
			side = nil
			live = map[Hash]bool{}
			for _, k := range keys {
				if _, ok := tr.Get(k); ok {
					live[k] = true
				}
			}
			record(fmt.Sprintf("compact %d", b), nil)
		}
	}
	lastPruned = 0
	for _, tw := range tr.twigs {
		if tw != nil && tw.pruned {
			lastPruned++
		}
	}
	return steps, checked
}

// lastPruned: pruned twigs at the end of the last run (coverage report).
var lastPruned int

// checkTwigSetSuperset asserts the set's invariant: every twig either walk
// can act on is noted (flag set and id present).
func checkTwigSetSuperset(t *testing.T, tr *Tree, what string) {
	t.Helper()
	if !tr.tset.on {
		return
	}
	in := map[int]bool{}
	for _, id := range tr.tset.ids {
		in[id] = true
	}
	for id, tw := range tr.twigs {
		if tw == nil {
			continue
		}
		if tw.inSet && !in[id] {
			t.Fatalf("%s: twig %d flagged but not in the set", what, id)
		}
		if inTwigSetRule(tw) && !tw.inSet {
			t.Fatalf("%s: twig %d (resident=%v pruned=%v metaDirty=%v) missing from the set",
				what, id, tw.nodes != nil, tw.pruned, tw.metaDirty)
		}
	}
}

// TestFlushTwigSetDifferential: random long workloads with the switch off,
// on and in check mode write byte-identical rows in the same order, reach
// the same roots and leave the same resident/pruned/meta-dirty twigs after
// every step; the check mode never fires (it panics in a test binary).
func TestFlushTwigSetDifferential(t *testing.T) {
	cfgs := []twigSetRun{
		{seed: 1, blocks: 260, opsPer: 900, lagK: 0},
		{seed: 2, blocks: 260, opsPer: 900, lagK: 4},
		{seed: 3, blocks: 200, opsPer: 1400, lagK: 4, retained: 3000},
		{seed: 4, blocks: 200, opsPer: 1400, lagK: 0, shard: true},
	}
	if testing.Short() {
		for i := range cfgs {
			cfgs[i].blocks /= 4
		}
	}
	for _, cfg := range cfgs {
		cfg := cfg
		t.Run(fmt.Sprintf("seed%d_lag%d_ret%d_shard%v", cfg.seed, cfg.lagK, cfg.retained, cfg.shard), func(t *testing.T) {
			off, _ := cfg.run(t, "")
			prunedAtEnd := lastPruned
			on, _ := cfg.run(t, "1")
			chk, checked := cfg.run(t, "check")
			if checked == 0 {
				t.Fatal("check mode compared nothing")
			}
			kinds := map[string]int{}
			for i := range off {
				kinds[firstWord(off[i].what)]++
			}
			for name, other := range map[string][]twigSetStep{"on": on, "check": chk} {
				if len(other) != len(off) {
					t.Fatalf("%s: %d steps, off %d", name, len(other), len(off))
				}
				for i := range off {
					if off[i].what != other[i].what || off[i].digest != other[i].digest || off[i].rows != other[i].rows {
						t.Fatalf("%s differs from off at step %d (%s, %d rows) vs (%s, %d rows)",
							name, i, other[i].what, other[i].rows, off[i].what, off[i].rows)
					}
				}
			}
			t.Logf("steps %v, checked walks %d, pruned twigs at end %d", kinds, checked, prunedAtEnd)
		})
	}
}

func firstWord(s string) string {
	if i := bytes.IndexByte([]byte(s), ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// TestFlushTwigSetCheckCatchesMiss: a twig that enters the walks' scope
// without being noted (here: a direct field write bypassing the hooks) is
// caught by the check mode.
func TestFlushTwigSetCheckCatchesMiss(t *testing.T) {
	defer SetFlushTwigSetForTest("check")()
	st := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(st))
	tr.SetLeafStore(LeafStoreFromGetter(st))
	var flushed uint64
	for b := 0; b < 4; b++ {
		for i := 0; i < TwigSize; i++ {
			var k Hash
			binary.BigEndian.PutUint64(k[:], uint64(b*TwigSize+i+1))
			tr.Set(k, []byte{1})
		}
		rec := &rowRec{}
		next, _, err := tr.FlushTo(rec, flushed)
		if err != nil {
			t.Fatal(err)
		}
		rec.applyTo(t, st)
		tr.CommitFlush()
		flushed = next
		tr.EvictTwigsThrough(flushed)
	}
	tw := tr.twigs[0]
	if tw.nodes != nil || tw.metaDirty {
		t.Fatalf("twig 0 should be evicted and clean")
	}
	tw.metaDirty = true // bypasses setTwigBit's note
	before := TwigSetMismatches()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("check mode did not catch the unnoted twig")
			}
		}()
		_, _, _ = tr.FlushTo(&rowRec{}, flushed)
	}()
	if TwigSetMismatches() != before+1 {
		t.Fatal("mismatch not counted")
	}
}
