// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Sharded apply (r39a). The S72 parallel pre-pass (apply_parallel.go) moved
// the index probe, the undo copy and the leaf hash onto workers but kept the
// whole commit on one goroutine, and that commit's index Put then paid the
// DRAM miss the probe had just paid on another core: on a 6M-key
// follower-shaped block it is SLOWER than the plain loop (23 vs 17 ms,
// BenchmarkShardApply). This path parallelizes the commit as well, without
// changing any output byte.
//
// Within one batch of strictly ascending (hence distinct) keys:
//
//   - op i's new slot is nextSlot + (number of sets before i): a prefix count
//     fixes every append's slot before anything is written, so workers over
//     contiguous op chunks write disjoint entry-window elements, disjoint twig
//     leaves and disjoint AppendedKeys positions;
//   - with the sharded in-RAM index (index_sharded.go) chunk boundaries fall
//     on first-byte shard boundaries, so each worker's index Puts/Deletes go
//     to maps no other worker touches; the other in-RAM shapes take their
//     writes serially, in op order, after the workers finish;
//   - everything whose order or sharing matters stays on the calling
//     goroutine, in op order: undo Entries (and cold old-value reads, whose
//     reader is the batch's thread-bound MDBX RwTx), deactivations (liveness
//     bits, deadFlushed, death stamps), the new slots' liveness bits and the
//     touched-twig sets, and history key versions.
//
// Old slots all lie below the batch's first new slot, so the deactivations
// and the appends never touch the same entry or bit; bit flips commute, and
// the touched sets are sets. The fold that follows is the sequential path's
// own (EndLeafBatch + Root) over the same touched leaves. Root, undo record,
// persisted rows, index contents and key-version order are therefore the
// sequential path's bytes (shard_apply_test.go checks all of them).

package qmdb

import (
	"bytes"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"
)

// ShardApplyWorkers is the worker count for ApplyOps' sharded path; 0 (the
// default, N42_QMDB_SHARD_APPLY unset or 0) keeps the existing path
// byte-for-byte. Read once at init; tests set it directly. Non-zero also
// makes NewIndexFor build the sharded in-RAM index.
var ShardApplyWorkers = func() int {
	n, err := strconv.Atoi(os.Getenv("N42_QMDB_SHARD_APPLY"))
	if err != nil || n < 0 {
		return 0
	}
	return min(n, 256)
}()

// shardApplyMinChunk is the smallest op range worth a worker.
const shardApplyMinChunk = 512

// ApplyPhases is the time split of a tree's most recent sharded ApplyOps
// (zero when that batch took another path).
type ApplyPhases struct {
	Workers int
	Probe   time.Duration // parallel: index probe, undo copy, leaf hash
	Serial  time.Duration // owner: undo record, deactivations, liveness bits, in-order index writes
	Commit  time.Duration // parallel: entries, values, leaves, appended keys, sharded index writes
	Fold    time.Duration // EndLeafBatch + Root
}

// LastApplyPhases reports the split of the tree's most recent ApplyOps.
func (t *Tree) LastApplyPhases() ApplyPhases { return t.lastApply }

// shardOp is one op's precomputed inputs.
type shardOp struct {
	old     uint64 // live slot before the batch (valid iff hasOld)
	leaf    Hash   // leaf hash of a set
	undoVal []byte // copy of the old entry's value (resident only)
	hasOld  bool
	undoOK  bool // undoVal is filled; otherwise the owner reads it
}

// shardChunk is one worker's contiguous op range and its derived offsets.
type shardChunk struct {
	lo, hi int
	sets   int    // sets in [lo, hi)
	olds   int    // ops in [lo, hi) with a live old slot
	bytes  int    // value bytes of those sets
	slot   uint64 // first new slot of the chunk
	off    int    // first value byte of the chunk
	// sharded-index counter totals, added to the package counters once
	hit, miss, puts, dels uint64
}

// shardApplyEligible reports whether a batch can take the sharded path:
// enough ops for two chunks, strictly ascending keys, an in-RAM index that
// reads safely from several goroutines, and resident, clean twigs under
// every new slot (an evicted or dirty append twig stays with the sequential
// path, which hydrates or defers it).
func (t *Tree) shardApplyEligible(ops []Op) bool {
	if len(ops) < 2*shardApplyMinChunk {
		return false
	}
	switch t.idx.(type) {
	case *shardedMapIndex, *flatIndex, mapIndex:
	default:
		return false
	}
	sets := 0
	for i := range ops {
		if i > 0 && bytes.Compare(ops[i-1].KeyHash[:], ops[i].KeyHash[:]) >= 0 {
			return false
		}
		if len(ops[i].Value) != 0 {
			sets++
		}
	}
	if sets == 0 {
		return true
	}
	if t.nextSlot < t.entriesBase {
		return false
	}
	first, last := int(t.nextSlot/TwigSize), int((t.nextSlot+uint64(sets)-1)/TwigSize)
	for id := first; id <= last && id < len(t.twigs); id++ {
		if tw := t.twigs[id]; tw.nodes == nil || tw.dirty {
			return false
		}
	}
	return true
}

// shardChunks splits ops into at most `workers` contiguous chunks. With the
// sharded index a boundary only falls where the first key byte changes, so
// no two chunks write the same index shard.
func shardChunks(ops []Op, workers int, onShards bool) []shardChunk {
	workers = max(1, min(workers, len(ops)/shardApplyMinChunk))
	chunks := make([]shardChunk, 0, workers)
	lo := 0
	for w := 1; w <= workers && lo < len(ops); w++ {
		hi := len(ops)
		if w < workers {
			hi = max(lo+1, w*len(ops)/workers)
			if onShards {
				for hi < len(ops) && ops[hi].KeyHash[0] == ops[hi-1].KeyHash[0] {
					hi++
				}
			}
		}
		chunks = append(chunks, shardChunk{lo: lo, hi: hi})
		lo = hi
	}
	return chunks
}

// runChunks runs fn over every chunk, the first on the calling goroutine.
func runChunks(chunks []shardChunk, fn func(c *shardChunk)) {
	var wg sync.WaitGroup
	for i := 1; i < len(chunks); i++ {
		wg.Add(1)
		go func(c *shardChunk) {
			defer wg.Done()
			fn(c)
		}(&chunks[i])
	}
	fn(&chunks[0])
	wg.Wait()
}

// shardProbe fills pre[c.lo:c.hi] and the chunk's set and byte counts.
// Read-only on the tree.
func (t *Tree) shardProbe(ops []Op, pre []shardOp, c *shardChunk, sx *shardedMapIndex, undo bool) {
	var (
		buf  [2048]byte
		lens [16]uint32
		outs [16]Hash
		pend [16]int
		np   int
	)
	flush := func() {
		if np == 16 {
			hashLeaves16(&outs, &buf, &lens)
			for m := 0; m < 16; m++ {
				pre[pend[m]].leaf = outs[m]
			}
		} else {
			for m := 0; m < np; m++ {
				pre[pend[m]].leaf = hashLeaf(ops[pend[m]].KeyHash, ops[pend[m]].Value)
			}
		}
		np = 0
	}
	for i := c.lo; i < c.hi; i++ {
		p := &pre[i]
		kh := ops[i].KeyHash
		if sx != nil {
			p.old, p.hasOld = sx.get(kh)
			if p.hasOld {
				c.hit++
			} else {
				c.miss++
			}
		} else {
			p.old, p.hasOld = t.idx.Get(kh)
		}
		if p.hasOld {
			c.olds++
		}
		if undo && p.hasOld && p.old >= t.entriesBase {
			if j := p.old - t.entriesBase; j < uint64(len(t.entries)) {
				v := t.entries[j].value
				p.undoVal = make([]byte, len(v))
				copy(p.undoVal, v)
				p.undoOK = true
			}
		}
		v := ops[i].Value
		if len(v) == 0 {
			continue
		}
		c.sets++
		c.bytes += len(v)
		// The two-block sizes go 16 per AVX-512 pass, as the sequential
		// path's deferred leaf jobs do; the rest hash one by one.
		if total := 33 + len(v); haveAVX512 && total > 64 && total <= 128 {
			lane := buf[np*128 : (np+1)*128]
			lane[0] = 0x01
			copy(lane[1:33], kh[:])
			n := copy(lane[33:], v)
			clear(lane[33+n:]) // lanes are reused across flushes
			lens[np] = uint32(total - 64)
			pend[np] = i
			np++
			if np == 16 {
				flush()
			}
			continue
		}
		p.leaf = hashLeaf(kh, v)
	}
	flush()
}

// applyOpsSharded is ApplyOps over `workers` goroutines (see the file
// comment). The caller checked shardApplyEligible.
func (t *Tree) applyOpsSharded(ops []Op, workers int) {
	t0 := time.Now()
	sx, _ := t.idx.(*shardedMapIndex)
	chunks := shardChunks(ops, workers, sx != nil)
	pre := make([]shardOp, len(ops))
	undo := t.rec != nil
	runChunks(chunks, func(c *shardChunk) { t.shardProbe(ops, pre, c, sx, undo) })
	t1 := time.Now()

	// Owner: offsets and window growth.
	base := t.nextSlot
	sets, olds, total := 0, 0, 0
	for i := range chunks {
		c := &chunks[i]
		c.slot = base + uint64(sets)
		c.off = total
		sets += c.sets
		olds += c.olds
		total += c.bytes
	}
	end := base + uint64(sets)
	if sets > 0 {
		t.twigFor(end - 1)
		// Every element of the new range is written by the commit, so the
		// window grows by reslicing into its spare capacity.
		if need := int(end - t.entriesBase); len(t.entries) < need {
			t.entries = slices.Grow(t.entries, need-len(t.entries))[:need]
		}
	}
	keyOff := 0
	if undo {
		keyOff = len(t.rec.AppendedKeys)
		t.rec.AppendedKeys = slices.Grow(t.rec.AppendedKeys, sets)[:keyOff+sets]
		t.rec.Entries = slices.Grow(t.rec.Entries, olds)
	}
	// One value block per batch instead of arena views: the arena's only
	// contract is stable views, which a block shared by the batch keeps.
	vals := make([]byte, total)
	t.BeginLeafBatch()
	t2 := time.Now()

	// Workers commit the chunks' appends (and, on the sharded index, their
	// index writes) while the owner records the undo entries and deactivates
	// the old slots in op order. Old slots lie below `base`, so the two sides
	// write disjoint entries and disjoint twig fields, and the owner's cold
	// reads do not touch the index (recordDeactivationCold).
	var wg sync.WaitGroup
	for i := range chunks {
		wg.Add(1)
		go func(c *shardChunk) {
			defer wg.Done()
			t.shardCommit(ops, pre, c, sx, vals, base, keyOff, undo)
		}(&chunks[i])
	}
	for i := range ops {
		p := &pre[i]
		if !p.hasOld {
			continue
		}
		if p.undoOK {
			t.rec.Entries = append(t.rec.Entries, UndoEntry{Slot: p.old, KeyHash: ops[i].KeyHash, Value: p.undoVal})
		} else {
			t.recordDeactivationCold(p.old, ops[i].KeyHash)
		}
		t.deactivate(p.old)
	}
	t3 := time.Now()
	wg.Wait()
	t4 := time.Now()

	// Owner: the new slots' liveness bits and touched sets, then whatever
	// must stay in op order.
	for s := base; s < end; {
		id := int(s / TwigSize)
		tw := t.twigs[id]
		stop := min(end, uint64(id+1)*TwigSize)
		tw.live += int(stop - s)
		for ; s < stop; s++ {
			tw.setBit(s%TwigSize, true)
			t.batchTouched = append(t.batchTouched, s)
		}
		t.setTwigBit(tw, id, (stop-1)%TwigSize, true) // already set: marks the twig touched
	}
	if sx != nil {
		var hit, miss, puts, dels uint64
		for i := range chunks {
			hit += chunks[i].hit
			miss += chunks[i].miss
			puts += chunks[i].puts
			dels += chunks[i].dels
		}
		idxGetHit.Add(hit)
		idxGetMiss.Add(miss)
		idxPuts.Add(puts)
		idxDeletes.Add(dels)
	}
	if sx == nil || t.hist != nil {
		slot := base
		for i := range ops {
			kh := ops[i].KeyHash
			if len(ops[i].Value) == 0 {
				if sx == nil && pre[i].hasOld {
					t.idx.Delete(kh)
				}
				continue
			}
			if sx == nil {
				t.idx.Put(kh, slot)
			}
			if t.hist != nil {
				if err := t.hist.store.AppendKeyVersion(kh, slot); err != nil {
					t.hist.appendErr = err
				}
			}
			slot++
		}
	}
	if sets > 0 {
		t.nextSlot = end
		t.rootDirty = true
	}
	t5 := time.Now()
	t.EndLeafBatch()
	t.Root()
	t.lastApply = ApplyPhases{
		Workers: len(chunks),
		Probe:   t1.Sub(t0),
		Serial:  t2.Sub(t1) + t3.Sub(t2) + t5.Sub(t4),
		Commit:  t4.Sub(t2),
		Fold:    time.Since(t5),
	}
}

// shardCommit writes chunk c's appends: entries, values, leaves, appended
// keys, and on the sharded index its own shards' Puts and Deletes.
func (t *Tree) shardCommit(ops []Op, pre []shardOp, c *shardChunk, sx *shardedMapIndex, vals []byte, base uint64, keyOff int, undo bool) {
	slot, off := c.slot, c.off
	for i := c.lo; i < c.hi; i++ {
		kh := ops[i].KeyHash
		src := ops[i].Value
		if len(src) == 0 {
			if sx != nil && pre[i].hasOld {
				sx.del(kh)
				c.dels++
			}
			continue
		}
		v := vals[off : off+len(src) : off+len(src)]
		copy(v, src)
		off += len(src)
		t.entries[slot-t.entriesBase] = entry{keyHash: kh, value: v, active: true}
		t.twigs[slot/TwigSize].nodes[TwigSize+slot%TwigSize] = pre[i].leaf
		if undo {
			t.rec.AppendedKeys[keyOff+int(slot-base)] = kh
		}
		if sx != nil {
			sx.put(kh, slot)
			c.puts++
		}
		slot++
	}
}

// recordDeactivationCold is recordDeactivation for the sharded apply's owner
// while workers write the index: a cold slot's value comes straight from the
// cold reader, without entryAt's index probe (that probe only derives the
// liveness flag, which the undo record does not carry). Same entries, same
// poisoning.
func (t *Tree) recordDeactivationCold(slot uint64, keyHash Hash) {
	if t.rec == nil {
		return
	}
	var (
		v  []byte
		ok bool
	)
	if slot >= t.entriesBase {
		if i := slot - t.entriesBase; i < uint64(len(t.entries)) {
			noteResidentHit(slot)
			v, ok = t.entries[i].value, true
		}
	} else if t.cold != nil {
		coldReads.Add(1)
		_, v, ok = t.cold.ColdEntry(slot)
	}
	if !ok {
		if !t.rec.broken {
			t.rec.brokenSlot = slot
			t.rec.brokenKey = keyHash
		}
		t.rec.broken = true
		return
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	t.rec.Entries = append(t.rec.Entries, UndoEntry{Slot: slot, KeyHash: keyHash, Value: cp})
}
