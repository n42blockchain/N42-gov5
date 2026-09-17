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
	"github.com/n42blockchain/N42/lib/trie"
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
	// In --leaf-seg mode the chg rows and the ACCOUNT node records go to the
	// segment spill like the leaf rows do (write-once; the account-side
	// FULL/DIFF bookkeeping lives entirely in accLastFull, so the builder
	// never reads them back). Storage node records stay in MDBX — the
	// lastFullCache read-back needs them queryable.
	if b.spill != nil {
		for _, e := range []struct {
			tab int
			buf *[]kvPair
		}{
			{segTabChgA, &b.chgAccBuf}, {segTabChgS, &b.chgStoBuf}, {segTabNodeA, &b.nodeAccBuf},
		} {
			for i := range *e.buf {
				if err := b.spill.add(e.tab, (*e.buf)[i].k, (*e.buf)[i].v); err != nil {
					return err
				}
			}
			*e.buf = (*e.buf)[:0]
		}
	}
	if b.spill != nil {
		for i := range b.stoRootBuf {
			if err := b.spill.add(segTabStoRoot, b.stoRootBuf[i].k, b.stoRootBuf[i].v); err != nil {
				return err
			}
		}
		b.stoRootBuf = b.stoRootBuf[:0]
	}
	for _, e := range []struct {
		table string
		buf   *[]kvPair
	}{
		{tDatcAccChg, &b.chgAccBuf}, {tDatcStoChg, &b.chgStoBuf},
		{tDatcLeafA, &b.leafABuf}, {tDatcLeafS, &b.leafSBuf},
		{tDatcStoRoot, &b.stoRootBuf},
		{tDatcAccNode, &b.nodeAccBuf}, {tDatcStoNode, &b.nodeStoBuf},
		{tDatcStoDepth, &b.stoDepthBuf},
	} {
		if err := flushBuf(tx, e.table, e.buf); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) maybeEarlyFlush(tx kv.RwTx) error {
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
	blk4 [blkLen]byte) error {
	for addr, acct := range dirtyA {
		ah := b.addrHash(addr)
		var val []byte
		if acct != nil {
			val = acct.MarshalV2()
		}
		if err := b.putLeaf(false, append(append([]byte{}, ah[:]...), blk4[:]...), val); err != nil {
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
		domain := ah[:] // storage domain = addrHash (32B)
		for slot, v := range slots {
			sh := b.slotHash(slot)
			composite := make([]byte, 0, stoDomainLen+32+blkLen)
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
			if err := b.putLeaf(true, append(composite, blk4[:]...), val); err != nil {
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
// Change rows are written for every level whose epoch is longer than one
// block AND that has node records: the account-trie root (d0) has no record
// (it is synthesized from its depth-1 children), so account d0 rows are never
// written; storage tries DO have d0 (root) records, so storage d0 rows are
// written whenever E_0 > 1 — the reader needs them to tell a mid-epoch
// storage root apart from the previous epoch's record.
func (b *builder) recordChange(storage bool, domain []byte, keyNibbles []byte, n uint64) {
	if storage {
		b.recordChangeStorage(domain, keyNibbles, n)
		return
	}
	// Levels with records: 0..accDepth-1 (the reader folds at accDepth).
	maxD := b.accDepth - 1
	if maxD > maxChgDepth {
		maxD = maxChgDepth
	}
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
		b.accLastChg[d][idx] = uint32(n)
		if l := b.sched.lenFor(false, d); l > 1 {
			epoch := uint32(n / l)
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
	// Levels with records: 0..depth-1 for THIS contract (the reader folds at
	// depth). A contract whose whole history fits inside one fold gets depth 0
	// and no records at all.
	b.noteStoDepth(domain, n)
	maxD := b.stoDepthFor(domain) - 1
	if maxD < 0 {
		return
	}
	if maxD > maxChgDepth {
		maxD = maxChgDepth
	}
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
		if b.sched.lenFor(true, d) == 1 {
			continue // per-block level: the floor record is exact, no window
		}
		epoch := b.sched.epochOfFor(true, d, n)
		// The aggregation key needs its OWN buffer: writing the 4 epoch bytes
		// into kb would land on kb[1+len(domain)+d], which is where level d+1
		// reads its path nibble — every deeper level then keyed its dirty path
		// with the epoch's high byte (0) instead of the nibble, so only child 0
		// ever got a node record and the reader folded the other 15 subtrees.
		ab := b.chgAggScratch[:0]
		ab = append(ab, byte(d))
		ab = append(ab, domain...)
		ab = append(ab, keyNibbles[:d]...)
		ab = binary.BigEndian.AppendUint32(ab, uint32(epoch))
		b.chgAggScratch = ab
		ak := ab
		if p, ok := b.chgStoAgg[string(ak)]; ok {
			*p = append(*p, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
		} else {
			evs := make([]chgEvent, 0, 8)
			evs = append(evs, chgEvent{block: uint32(n), nibble: keyNibbles[d]})
			b.chgStoAgg[string(ak)] = &evs
		}
		b.chgPuts++
		b.chgStoPuts++
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
	// Account side: drain the flat slots via the touched lists and reset them
	// (level 0 included: the root's change rows exist when --acc-root-epoch
	// > 1; an open epoch's partial row is keyed by its first block like any
	// other level's).
	for d := 0; d <= maxChgDepth; d++ {
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
}

// flushEpoch persists the epoch-end node bytes for every path changed during
// the closing epoch of level d, reading the CURRENT TrieOf* rows.
func (b *builder) flushEpoch(tx kv.RwTx, d int, epoch uint64) error {
	if err := b.flushAccLevel(tx, d, epoch); err != nil {
		return err
	}
	return b.flushStoLevel(tx, d, epoch)
}

// flushAccLevel persists the account-trie node records of level d for the
// closing epoch. The account and storage ladders can differ, so the build
// loop calls this and flushStoLevel separately, each with its own epoch.
func (b *builder) flushAccLevel(tx kv.RwTx, d int, epoch uint64) error {
	// Account side: sorted dense indices (numeric order == path order for a
	// fixed level), path reconstructed from the index. Level 0 (the root) is
	// owned by flushAccRoot (its own cadence, dense-hook sourced).
	if d > 0 && len(b.accTouched[d]) > 0 {
		touched := b.accTouched[d]
		sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
		path := make([]byte, d)
		for _, idx := range touched {
			changed := b.accDirty[d][idx]
			b.accDirty[d][idx] = 0
			if changed == 0 {
				continue
			}
			v := idx
			for i := d - 1; i >= 0; i-- {
				path[i] = byte(v & 0xf)
				v >>= 4
			}
			if err := b.flushAccPath(tx, path, changed, epoch, uint64(b.accLastChg[d][idx])); err != nil {
				return err
			}
		}
		b.accTouched[d] = touched[:0]
	}
	return nil
}

// flushAccRoot records the account-trie root node (no TrieOfAccounts row
// exists for it; the bytes come from the loader's dense hook) when any
// account changed since the last root record.
func (b *builder) flushAccRoot(tx kv.RwTx, epoch uint64) error {
	changed := b.accDirty[0][0]
	b.accDirty[0][0] = 0
	b.accTouched[0] = b.accTouched[0][:0]
	if changed == 0 {
		return nil
	}
	return b.flushAccPath(tx, []byte{}, changed, epoch, uint64(b.accLastChg[0][0]))
}

// nodeUsable reports whether a TrieOf* node can ever be assembled by the
// reader: every present child must carry a stored hash (a plain branch of
// hashed children). Nodes with leaf/extension children are folded from the
// leaf history instead, so their masks and hashes are never read.
func nodeUsable(node []byte) bool {
	if len(node) < 6 {
		return false
	}
	hasState, _, hasHash, _, _ := trie.UnmarshalTrieNode(node)
	return hasState != 0 && hasState == hasHash
}

// flushAccPath emits one account-trie node record (FULL/DIFF/MIXED/tombstone).
// lastChg is the last block at which anything under the node changed.
func (b *builder) flushAccPath(tx kv.RwTx, path []byte, changed uint16, epoch, lastChg uint64) error {
	node, err := tx.GetOne(modules.TrieOfAccounts, path)
	if err != nil {
		return err
	}
	// Prefer the loader's dense form: complete child hashes even when the
	// TrieOf row is mixed (leaf/extension children) or absent (the root).
	//
	// Only when it is CURRENT, though. The loader reports a branch while it
	// walks it, and once erigon drops the node's TrieOf row it can stop
	// reporting for the rest of the epoch while the subtree keeps changing.
	// Recording that snapshot as the epoch-end state writes child hashes from
	// the wrong height under a correct changed-mask, and the reader has no way
	// to tell (2026-09-16: `--acc-depth 3 --sched 4,16,64,...` diverged from
	// height 208, and lengthening v3's depth-3/4 epochs reproduces it).
	staleDense := false
	if dn, dnBlk := b.takeDense(false, string(path)); dn != nil && (len(node) == 0 || !nodeUsable(node)) {
		if dnBlk >= lastChg {
			node = dn
			b.statDenseUpgraded++
		} else {
			staleDense = true
			b.statDenseStale++
		}
	}
	k := make([]byte, 0, 1+len(path)+4)
	k = append(k, byte(len(path)))
	k = append(k, path...)
	k = binary.BigEndian.AppendUint32(k, uint32(epoch))
	st := b.accLastFull[string(path)]
	var v []byte
	switch {
	case staleDense:
		// No trustworthy epoch-end bytes: MIXED tells the reader to fold this
		// node from the leaf history, which is always right (just slower than
		// a record). Never a tombstone — the node is live, only unreported.
		if st.mixed && !b.resumed {
			b.statMixedElided++
			return nil
		}
		v = []byte{nodeRecMixed}
		b.accLastFull[string(path)] = nodeRecState{mixed: true}
	case len(node) == 0: // tombstone
		if !st.exists && !b.resumed {
			return nil // never had a live record: elide
		}
		b.accLastFull[string(path)] = nodeRecState{exists: false}
	case !nodeUsable(node):
		if st.mixed && !b.resumed {
			b.statMixedElided++
			return nil // still mixed: the floor already says "fold"
		}
		v = []byte{nodeRecMixed}
		b.statMixedBytesSaved += uint64(len(node))
		b.accLastFull[string(path)] = nodeRecState{mixed: true}
	case !st.exists || st.diffs >= fullEvery-1:
		v = append([]byte{nodeRecFull}, node...)
		b.accLastFull[string(path)] = nodeRecState{exists: true}
	default:
		v = encodeNodeDiff(node, changed)
		st.diffs++
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
			// Full-key read works for the DupSort table: the kv layer auto-
			// converts keys exactly as trie_root_computer's own Put/Delete do.
			node, err := tx.GetOne(srcTable, path)
			if err != nil {
				return err
			}
			// The storage side does NOT take the loader's dense form: the
			// synthesized node does not match the epoch-end node here, and a
			// record built from it hashes differently from the leaf-history
			// fold (TestDiagStorageChildHashes / TestE2E_PerBlock_E0is4 catch
			// it). A mixed node stays MIXED and the reader folds it. This path
			// was unreachable until the dirty-key fix above, because the dirty
			// key carried a zeroed nibble while the hook keys the true one.
			b.takeDense(true, pk) // drain the hook entry either way
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
			case !nodeUsable(node):
				if st.mixed {
					b.statMixedElided++
					continue // still mixed: the floor already says "fold"
				}
				v = []byte{nodeRecMixed}
				b.statMixedBytesSaved += uint64(len(node))
				putLF(pk, nodeRecState{mixed: true})
			case !st.exists || degraded || st.diffs >= fullEvery-1:
				v = append([]byte{nodeRecFull}, node...)
				putLF(pk, nodeRecState{exists: true})
			default:
				v = encodeNodeDiff(node, changed)
				st.diffs++
				putLF(pk, st)
			}
			*buf = append(*buf, kvPair{k: k, v: v})
			b.nodePuts++
			b.nodeStoPuts++
		}
		return nil
	}
}
