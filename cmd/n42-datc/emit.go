// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// emit.go — the builder's record-emission and flush path: per-block leaf
// history + change events (emitBlock), the flat-indexed change bitmaps
// (recordChange/recordChangeStorage), epoch-boundary node records
// (flushEpoch/flushAccPath/flushStoLevel), and the sorted-batch writers
// (flushBuf/flushAllBufs/maybeEarlyFlush). All write into the DATC tables /
// leaf-seg spill; none touch the trie (the gold-check root lives in builder.go).

package main

import (
	"encoding/binary"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// flushBuf sorts a buffer by key and applies it to the table sequentially.
func flushBuf(tx kv.RwTx, table string, buf *[]kvPair) error {
	if len(*buf) == 0 {
		return nil
	}
	sort.Slice(*buf, func(i, j int) bool { return string((*buf)[i].k) < string((*buf)[j].k) })
	for i := range *buf {
		if err := tx.Put(table, (*buf)[i].k, (*buf)[i].v); err != nil {
			return err
		}
	}
	*buf = (*buf)[:0]
	return nil
}

// flushAllBufs drains every sorted-batch buffer into the tx.
func (b *builder) flushAllBufs(tx kv.RwTx) error {
	// In --leaf-seg mode the chg rows go to the segment spill like the leaf
	// rows do (write-once, never read back by the builder; StorChg was the
	// LARGEST table of the 6M calibration). Node records stay in MDBX — the
	// lastFullCache read-back needs them queryable.
	if b.spill != nil {
		for _, e := range []struct {
			tab int
			buf *[]kvPair
		}{
			{segTabChgA, &b.chgAccBuf}, {segTabChgS, &b.chgStoBuf},
		} {
			for i := range *e.buf {
				if err := b.spill.add(e.tab, (*e.buf)[i].k, (*e.buf)[i].v); err != nil {
					return err
				}
			}
			*e.buf = (*e.buf)[:0]
		}
	}
	for _, e := range []struct {
		table string
		buf   *[]kvPair
	}{
		{tDatcAccChg, &b.chgAccBuf}, {tDatcStoChg, &b.chgStoBuf},
		{tDatcLeafA, &b.leafABuf}, {tDatcLeafS, &b.leafSBuf},
		{tDatcAccNode, &b.nodeAccBuf}, {tDatcStoNode, &b.nodeStoBuf},
		{tDatcStoRoot, &b.stoRootBuf},
	} {
		if err := flushBuf(tx, e.table, e.buf); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) maybeEarlyFlush(tx kv.RwTx) error {
	// Cap the unbounded storage change-aggregation map: drain it to the (compact,
	// length-capped) sorted buffers, which the buffer check below then flushes.
	if b.chgAggCapBytes > 0 && b.chgStoAggBytes > b.chgAggCapBytes {
		b.flushChgAgg()
	}
	if len(b.chgAccBuf) > bufFlushThreshold || len(b.chgStoBuf) > bufFlushThreshold ||
		len(b.leafABuf) > bufFlushThreshold || len(b.leafSBuf) > bufFlushThreshold {
		return b.flushAllBufs(tx)
	}
	return nil
}

// emitBlock writes one block's leaf-history rows + change events for the
// dirty maps (shared by the per-block and window paths).
func (b *builder) emitBlock(n uint64,
	dirtyA map[types.Address]*account.StateAccount, dirtyS map[types.Address]map[types.Hash]*uint256.Int,
	blk8 [8]byte) error {
	for addr, acct := range dirtyA {
		ah := b.addrHash(addr)
		var val []byte
		if acct != nil {
			val = acct.MarshalV2()
		}
		if err := b.putLeaf(false, append(append([]byte{}, ah[:]...), blk8[:]...), val); err != nil {
			return err
		}
		b.leafAPuts++
		b.recordChange(false, nil, nibblesOf(ah[:]), n)
	}
	for addr, slots := range dirtyS {
		ah := b.addrHash(addr)
		// A storage-only change still changes the account-trie leaf (its
		// storageRoot): mark the account path in the change index even though
		// acctcs has no entry for it. (TrieRootComputer does the same when it
		// adds storage-dirty addresses to the account RetainList.) No DatcLeafA
		// entry is needed — the account VALUE is unchanged; the verifier's fold
		// recomputes the storageRoot from the storage leaf history.
		if _, also := dirtyA[addr]; !also {
			b.recordChange(false, nil, nibblesOf(ah[:]), n)
		}
		domain := make([]byte, 40)
		copy(domain, ah[:])
		// domain = addrHash(32) ‖ 8 zero bytes. The 8 trailing bytes are a
		// DATC-internal leaf-composite convention only (the 72-byte DatcLeafS
		// key = domain‖slotHash); they are NOT an incarnation — the system
		// dropped incarnation, and the physical HashedStorage/TrieOfStorage
		// keys are addrHash(32)-prefixed. Any read of those external tables
		// must use ah[:] (32 bytes), never this 40-byte domain.
		for slot, v := range slots {
			sh := b.slotHash(slot)
			composite := make([]byte, 0, 72+8)
			composite = append(composite, domain...)
			composite = append(composite, sh[:]...)
			var val []byte
			if v != nil && !v.IsZero() {
				bb := v.Bytes32()
				s := 0
				for s < 31 && bb[s] == 0 {
					s++
				}
				val = append([]byte{}, bb[s:]...)
			}
			if err := b.putLeaf(true, append(composite, blk8[:]...), val); err != nil {
				return err
			}
			b.leafSPuts++
			b.recordChange(true, domain, nibblesOf(sh[:]), n)
		}
	}
	return nil
}

// recordChange records one dirty key: the changed-children bitmap per ancestor
// path (drives node diff records) and an aggregated change event per level.
//
// d0 change rows are deliberately NOT written: E_0 = 1 means the floor record
// for any query already sits at block N itself (epoch == block), so the d0
// change window is empty by construction and the verifier never reads it.
func (b *builder) recordChange(storage bool, domain []byte, keyNibbles []byte, n uint64) {
	if storage {
		b.recordChangeStorage(domain, keyNibbles, n)
		return
	}
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	// Account trie: dense flat index — idx_d = first d nibbles, built
	// incrementally. Zero allocations, zero hashing.
	idx := uint32(0)
	for d := 0; d <= maxD; d++ {
		bit := uint16(1) << keyNibbles[d]
		if cur := b.accDirty[d][idx]; cur == 0 {
			b.accTouched[d] = append(b.accTouched[d], idx)
			b.accDirty[d][idx] = bit
		} else if cur&bit == 0 {
			b.accDirty[d][idx] = cur | bit
		}
		if d != 0 && b.sched.e[d] != 1 {
			epoch := uint32(b.sched.epochOf(d, n))
			slot := &b.chgAccAgg[d][idx]
			if len(slot.events) > 0 && slot.epoch != epoch {
				// Epoch rolled over inside the batch: drain the closed epoch's
				// row inline.
				b.drainAccSlot(d, idx, slot)
			}
			if len(slot.events) == 0 {
				if slot.events == nil {
					b.chgAccAggTouched[d] = append(b.chgAccAggTouched[d], idx)
				}
				slot.epoch = epoch
				if slot.events == nil {
					slot.events = make([]chgEvent, 0, 8)
				}
			}
			slot.events = append(slot.events, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgPuts++
		}
		idx = idx*16 + uint32(keyNibbles[d])
	}
}

// recordChangeStorage is the sparse-domain (per-contract) variant: maps stay,
// but with pointer values — repeated touches are alloc-free lookups.
func (b *builder) recordChangeStorage(domain []byte, keyNibbles []byte, n uint64) {
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	// One shared key buffer: d(1) | domain | path nibbles | epoch(4); the
	// dirty key is the [1:1+len(domain)+d] slice, the agg key the whole thing.
	kb := b.chgKeyScratch[:0]
	kb = append(kb, 0)
	kb = append(kb, domain...)
	kb = append(kb, keyNibbles[:maxD]...)
	kb = append(kb, 0, 0, 0, 0)
	b.chgKeyScratch = kb
	for d := 0; d <= maxD; d++ {
		bit := uint16(1) << keyNibbles[d]
		pk := kb[1 : 1+len(domain)+d]
		if p, ok := b.stoDirty[d][string(pk)]; ok {
			*p |= bit // alloc-free hot path
		} else {
			v := bit
			b.stoDirty[d][string(pk)] = &v
		}
		if d == 0 || b.sched.e[d] == 1 {
			continue // empty-window levels: change rows are never consulted
		}
		epoch := b.sched.epochOf(d, n)
		kb[0] = byte(d)
		ak := kb[:1+len(domain)+d+4]
		binary.BigEndian.PutUint32(ak[len(ak)-4:], uint32(epoch))
		if p, ok := b.chgStoAgg[string(ak)]; ok {
			*p = append(*p, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgStoAggBytes += chgEventSize // amortized event growth
		} else {
			evs := make([]chgEvent, 0, 8)
			evs = append(evs, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgStoAgg[string(ak)] = &evs
			// new entry: map bucket + key string + slice header/backing overhead.
			b.chgStoAggBytes += len(ak) + 8*chgEventSize + 80
		}
		b.chgPuts++
	}
}

// drainAccSlot encodes one closed account-agg slot into the sorted write
// buffer (prefix d|path|epoch4|firstBlock4) and resets the slot.
func (b *builder) drainAccSlot(d int, idx uint32, slot *chgSlot) {
	k := make([]byte, 0, 1+d+4+4)
	k = append(k, byte(d))
	// Reconstruct the d nibbles from the dense index (big-endian base 16).
	for i := d - 1; i >= 0; i-- {
		k = append(k, 0)
	}
	v := idx
	for i := d; i >= 1; i-- {
		k[i] = byte(v & 0xf)
		v >>= 4
	}
	k = binary.BigEndian.AppendUint32(k, slot.epoch)
	k = binary.BigEndian.AppendUint32(k, slot.events[0].block)
	b.chgAccBuf = append(b.chgAccBuf, kvPair{k: k, v: encodeChgRow(slot.events)})
	slot.events = slot.events[:0]
}

// flushChgAgg drains the per-batch aggregated change buffers into the sorted
// write buffers: one row per (prefix, batch segment), keyed by the segment's
// first block so an epoch spanning batches concatenates in block order.
func (b *builder) flushChgAgg() {
	// Account side: drain the flat slots via the touched lists and reset them.
	for d := 1; d <= maxChgDepth; d++ {
		for _, idx := range b.chgAccAggTouched[d] {
			slot := &b.chgAccAgg[d][idx]
			if len(slot.events) > 0 {
				b.drainAccSlot(d, idx, slot)
			}
			slot.events = nil // release; nil re-arms the touched marker
		}
		b.chgAccAggTouched[d] = b.chgAccAggTouched[d][:0]
	}
	// Storage side: pointer-valued map.
	for ak, events := range b.chgStoAgg {
		if len(*events) > 0 {
			k := make([]byte, 0, len(ak)+4)
			k = append(k, ak...)
			k = binary.BigEndian.AppendUint32(k, (*events)[0].block)
			b.chgStoBuf = append(b.chgStoBuf, kvPair{k: k, v: encodeChgRow(*events)})
		}
		delete(b.chgStoAgg, ak)
	}
	b.chgStoAggBytes = 0
}

// flushEpoch persists the epoch-end node bytes for every path changed during
// the closing epoch of level d, reading the CURRENT TrieOf* rows.
func (b *builder) flushEpoch(tx kv.RwTx, d int, epoch uint64) error {
	// Account side: sorted dense indices (numeric order == path order for a
	// fixed level), path reconstructed from the index.
	if len(b.accTouched[d]) > 0 {
		touched := b.accTouched[d]
		sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
		path := make([]byte, d)
		for _, idx := range touched {
			changed := b.accDirty[d][idx]
			b.accDirty[d][idx] = 0
			if changed == 0 || d == 0 {
				// d0 = the account-trie root: no TrieAccount row by convention;
				// the verifier synthesizes it from the depth-1 children.
				continue
			}
			v := idx
			for i := d - 1; i >= 0; i-- {
				path[i] = byte(v & 0xf)
				v >>= 4
			}
			if err := b.flushAccPath(tx, path, changed, epoch); err != nil {
				return err
			}
		}
		b.accTouched[d] = touched[:0]
	}
	return b.flushStoLevel(tx, d, epoch)
}

// flushAccPath emits one account-trie node record (FULL/DIFF/tombstone).
func (b *builder) flushAccPath(tx kv.RwTx, path []byte, changed uint16, epoch uint64) error {
	node, err := tx.GetOne(modules.TrieOfAccounts, path)
	if err != nil {
		return err
	}
	k := make([]byte, 0, 1+len(path)+4)
	k = append(k, byte(len(path)))
	k = append(k, path...)
	k = binary.BigEndian.AppendUint32(k, uint32(epoch))
	st := b.accLastFull[string(path)]
	var v []byte
	switch {
	case len(node) == 0: // tombstone
		if !st.exists && !b.resumed {
			return nil // never had a live record: elide
		}
		b.accLastFull[string(path)] = nodeRecState{exists: false}
	case !st.exists || st.lastFullEpoch >= accFullEvery-1:
		// Account-side FULL cadence counts RECORDS, not epochs (the reader's
		// floorRecordBefore walks the record chain, never epoch distance).
		// The old epoch-distance rule made EVERY record FULL for any node
		// changing less often than fullEvery blocks under a dense (e=1)
		// schedule — 529 B/record instead of ~45 (the 2M B′ measurement).
		// lastFullEpoch is repurposed on this side as the DIFFs-since-FULL
		// counter; a restart loses the map and safely degrades to FULL.
		v = append([]byte{nodeRecFull}, node...)
		b.accLastFull[string(path)] = nodeRecState{lastFullEpoch: 0, exists: true}
	default:
		v = encodeNodeDiff(node, changed)
		st.lastFullEpoch++
		st.exists = true
		b.accLastFull[string(path)] = st
	}
	b.nodeAccBuf = append(b.nodeAccBuf, kvPair{k: k, v: v})
	b.nodePuts++
	return nil
}

// flushStoLevel emits the storage-trie node records for one level's pending
// paths (sorted; pointer-valued map).
func (b *builder) flushStoLevel(tx kv.RwTx, d int, epoch uint64) error {
	pending := b.stoDirty[d]
	{
		if len(pending) == 0 {
			return nil
		}
		srcTable := modules.TrieOfStorage
		nodeTable := tDatcStoNode
		buf := &b.nodeStoBuf
		getLF := func(pk string) (nodeRecState, bool, error) {
			return b.stoLastFull.get(tx, nodeTable, pk, epoch)
		}
		putLF := func(pk string, st nodeRecState) {
			b.stoLastFull.put(pk, st)
		}
		// Sorted iteration: the TrieOf* GetOne reads walk the B-tree in key
		// order instead of map-random order.
		paths := make([]string, 0, len(pending))
		for pk := range pending {
			paths = append(paths, pk)
		}
		sort.Strings(paths)
		for _, pk := range paths {
			changed := *pending[pk]
			path := []byte(pk)
			delete(pending, pk)
			// Read the live storage-trie node from TrieOfStorage. The system
			// dropped incarnation: the trie computer keys TrieOfStorage by
			// addrHash(32)‖nibblePath with NO incarnation (storCollector in
			// modules/state/commitment/trie_root_computer.go). DATC's internal
			// `path` carries an 8-byte zero incarnation after the addrHash
			// (addrHash32‖inc8‖nibblePath) — a leaf-composite-only convention
			// that never appears in the physical key. TrieOfStorage is a PLAIN
			// DupSort (not AutoDupSort), so there is no key-length folding to
			// absorb the extra 8 bytes: a 40-byte-prefixed read misses every
			// node and the record is silently elided as a tombstone (this is
			// why DatcStorNode came out empty). Strip the incarnation for the
			// external read — same lesson as the HashedStorage SeekBothRange
			// reads in main.go.
			stoKey := make([]byte, 0, len(path)-8)
			stoKey = append(stoKey, path[:32]...)
			stoKey = append(stoKey, path[40:]...)
			node, err := tx.GetOne(srcTable, stoKey)
			if err != nil {
				return err
			}
			// DATC node record: pathLen(1) | domain|path | epoch(4) → record.
			// Empty value = tombstone; else flags byte (FULL | DIFF). A FULL is
			// forced when no prior record exists (or it was a tombstone) and at
			// least every fullEvery-th epoch, bounding the reader's walk-back.
			k := make([]byte, 0, 1+len(path)+4)
			k = append(k, byte(len(path)))
			k = append(k, path...)
			k = binary.BigEndian.AppendUint32(k, uint32(epoch))

			st, degraded, err := getLF(pk)
			if err != nil {
				return err
			}
			var v []byte
			switch {
			case len(node) == 0: // tombstone
				if !st.exists {
					// Never had a live record — a tombstone adds nothing (the
					// verifier treats absent and tombstone identically; the
					// storage cache reads back the committed truth, so elision
					// stays safe on resumed builds too).
					continue
				}
				putLF(pk, nodeRecState{exists: false})
			case !st.exists || degraded || uint32(epoch) >= st.lastFullEpoch+fullEvery:
				v = append([]byte{nodeRecFull}, node...)
				putLF(pk, nodeRecState{lastFullEpoch: uint32(epoch), exists: true})
			default:
				v = encodeNodeDiff(node, changed)
				st.exists = true
				putLF(pk, st)
			}
			*buf = append(*buf, kvPair{k: k, v: v})
			b.nodePuts++
		}
		return nil
	}
}
