// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// backfill.go — resume dirty-mark backfill.
//
// The node-flush pending sets (accDirty/accTouched, stoDirty) live only in
// memory and span epochs, so ANY restart (graceful or crash) that lands
// mid-epoch loses the marks accumulated since each level's epoch start. The
// next epoch-boundary flush then silently OMITS those paths' records (the 2M
// A/B measurement: 28 DatcStorNode rows lost across one hard kill). The
// reader tolerates a missing record via the leaf-history fold fallback, so
// the hole degrades query latency rather than correctness — but it compounds
// with every restart of a multi-day build.
//
// backfillDirty replays the changesets of [min_d epochStart(d), start)
// through the decode pipeline and re-applies ONLY the dirty marks:
//   - accDirty/accTouched and stoDirty get their bits back. Per-level block
//     filter: block n marks level d iff n falls inside start's d-epoch
//     (n >= start - start%e[d]); e[d]==1 levels carry no cross-restart state.
//   - chg agg events are NOT replayed: flushChgAgg drains them into the same
//     tx as every batch commit, so the rows covering [epochStart, start) are
//     already on disk — replaying would write duplicate rows.
//   - leaf rows / StoRoot / trie are untouched (already committed).
//
// accLastFull/stoLastFull stay cold: the resumed FULL-degradation and
// tombstone materialization remain (safe — row bloat only). This pass closes
// the omission hole, not the bloat.

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// backfillDirty rebuilds the in-memory dirty marks for every level whose
// current epoch started before `start`. Call once on resume, before the main
// build loop.
func (b *builder) backfillDirty(start uint64) error {
	from := start
	for d := 0; d <= maxChgDepth; d++ {
		if b.sched.e[d] > 1 {
			if es := start - start%b.sched.e[d]; es < from {
				from = es
			}
		}
		if b.stoSched.e[d] > 1 {
			if es := start - start%b.stoSched.e[d]; es < from {
				from = es
			}
		}
	}
	if from >= start {
		return nil // resume landed exactly on every level's epoch boundary
	}
	fmt.Printf("[datc] resume dirty-mark backfill: replaying changeset marks [%d, %d) (%d blocks)\n",
		from, start, start-from)
	t0 := time.Now()
	pipe := startDecodePipeline(b, from, start, 3)
	defer pipe.Stop()
	lastBeat := time.Now()
	for n := from; n < start; n++ {
		dec, err := pipe.Next(n)
		if err != nil {
			return fmt.Errorf("backfill block %d: %w", n, err)
		}
		for addr := range dec.dirtyA {
			ah, ok := dec.ahash[addr]
			if !ok {
				ah = b.addrHash(addr)
			}
			b.backfillAccMarks(nibblesOf(ah[:]), n, start)
		}
		for addr, slots := range dec.dirtyS {
			ah, ok := dec.ahash[addr]
			if !ok {
				ah = b.addrHash(addr)
			}
			if _, also := dec.dirtyA[addr]; !also {
				// A storage-only change still dirties the account leaf (its
				// storageRoot) — same rule as emitBlock.
				b.backfillAccMarks(nibblesOf(ah[:]), n, start)
			}
			var domain [40]byte // addrHash(32) ‖ 8 zero bytes, as recordChangeStorage builds it
			copy(domain[:], ah[:])
			for slot := range slots {
				sh, ok := dec.shash[slot]
				if !ok {
					sh = b.slotHash(slot)
				}
				b.backfillStoMarks(domain[:], nibblesOf(sh[:]), n, start)
			}
		}
		releaseDecodedBlock(dec)
		if time.Since(lastBeat) > 10*time.Second {
			fmt.Fprintf(os.Stderr, "[datc] backfill %d/%d (%.0f blk/s)\n",
				n-from, start-from, float64(n-from)/time.Since(t0).Seconds())
			lastBeat = time.Now()
		}
	}
	fmt.Printf("[datc] dirty-mark backfill done: %d blocks in %s\n",
		start-from, time.Since(t0).Round(time.Second))
	return nil
}

// backfillDirtyFromSegs rebuilds the same marks as backfillDirty but from the
// chg SEGMENTS (--backfill-segs, usually the fork-state src dir) instead of a
// changeset replay: the chg rows for each level's CURRENT epoch already carry
// exactly (path → changed child nibbles ≤ start), so a skip-scan — Seek each
// path group straight to the current epoch, skip all older rows — recovers the
// marks with zero per-key keccak and zero decode-pipeline RAM (the 2.64M-block
// replay this replaces was the resume OOM).
//
// Coverage note: d0 chg rows are never written on either side; account d0
// flushes emit no record by convention and storage d0 records are never read
// (the querier synthesizes every trie root from its depth-1 children), so the
// d0 marks are inconsequential.
func (b *builder) backfillDirtyFromSegs(start uint64) error {
	cache := newFrameLRU()
	t0 := time.Now()
	fmt.Printf("[datc] resume dirty-mark backfill from chg segments (%s)\n", b.backfillSegs)

	scan := func(tab int, storage bool) error {
		segs, ok, err := openLeafSegSet(b.backfillSegs, tab, cache)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("chg segments for table %d not found under %s", tab, b.backfillSegs)
		}
		sch := b.sched
		if storage {
			sch = b.stoSched
		}
		var marks, rows uint64
		for d := 1; d <= maxChgDepth; d++ {
			if sch.e[d] <= 1 {
				continue // dense level: no cross-restart state
			}
			curEpoch := uint32(sch.epochOf(d, start))
			c := segs.Cursor()
			k, v, err := c.Seek([]byte{byte(d)})
			for k != nil && err == nil && len(k) > 0 && k[0] == byte(d) {
				if len(k) < 1+8 {
					k, v, err = c.Next()
					continue
				}
				prefix := k[:len(k)-8] // [d][domain?][path]
				epoch := binary.BigEndian.Uint32(k[len(k)-8 : len(k)-4])
				if epoch < curEpoch {
					// Skip this path group straight to its current-epoch rows.
					seek := make([]byte, 0, len(prefix)+4)
					seek = append(seek, prefix...)
					seek = binary.BigEndian.AppendUint32(seek, curEpoch)
					k, v, err = c.Seek(seek)
					continue
				}
				if epoch == curEpoch {
					rows++
					// Decode events: uvarint block-delta + nibble, absolute blocks.
					blk := uint64(0)
					pos := 0
					for pos < len(v) {
						dlt, m := binary.Uvarint(v[pos:])
						if m <= 0 || pos+m >= len(v) {
							break
						}
						pos += m
						blk += dlt
						nib := v[pos]
						pos++
						if blk >= start {
							break // events at/after the resume point re-emit live
						}
						path := prefix[1:]
						if storage {
							pk := string(path) // domain(40) + d nibbles
							if p, ok := b.stoDirty[d][pk]; ok {
								*p |= 1 << nib
							} else {
								bit := uint16(1) << nib
								b.stoDirty[d][pk] = &bit
							}
						} else {
							idx := uint32(0)
							for _, pn := range path {
								idx = idx*16 + uint32(pn)
							}
							bit := uint16(1) << nib
							if cur := b.accDirty[d][idx]; cur == 0 {
								b.accTouched[d] = append(b.accTouched[d], idx)
								b.accDirty[d][idx] = bit
							} else if cur&bit == 0 {
								b.accDirty[d][idx] = cur | bit
							}
						}
						marks++
					}
					k, v, err = c.Next()
					continue
				}
				// epoch > curEpoch cannot exist for committed rows (< start); be
				// safe and move to the next path group.
				next := make([]byte, 0, len(prefix)+8)
				next = append(next, prefix...)
				next = append(next, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
				k, v, err = c.Seek(next)
			}
			c.Close()
			if err != nil {
				return err
			}
		}
		side := "acct"
		if storage {
			side = "sto"
		}
		fmt.Printf("[datc] backfill(segs) %s: %d rows → %d marks\n", side, rows, marks)
		return nil
	}
	if err := scan(segTabChgA, false); err != nil {
		return err
	}
	if err := scan(segTabChgS, true); err != nil {
		return err
	}
	fmt.Printf("[datc] dirty-mark backfill (segs) done in %s\n", time.Since(t0).Round(time.Second))
	return nil
}

// backfillAccMarks re-applies recordChange's account dirty marks for block n,
// restricted to levels whose current epoch (relative to `start`) contains n.
// chg agg events are intentionally not touched.
func (b *builder) backfillAccMarks(nibs []byte, n, start uint64) {
	maxD := maxChgDepth
	if maxD > len(nibs)-1 {
		maxD = len(nibs) - 1
	}
	idx := uint32(0)
	for d := 0; d <= maxD; d++ {
		if b.sched.e[d] > 1 && n >= start-start%b.sched.e[d] {
			bit := uint16(1) << nibs[d]
			if cur := b.accDirty[d][idx]; cur == 0 {
				b.accTouched[d] = append(b.accTouched[d], idx)
				b.accDirty[d][idx] = bit
			} else if cur&bit == 0 {
				b.accDirty[d][idx] = cur | bit
			}
		}
		idx = idx*16 + uint32(nibs[d])
	}
}

// backfillStoMarks is the storage-side twin: stoDirty keys are
// domain(40) ‖ first-d-nibbles, exactly as recordChangeStorage builds them.
func (b *builder) backfillStoMarks(domain []byte, slotNibs []byte, n, start uint64) {
	maxD := maxChgDepth
	if maxD > len(slotNibs)-1 {
		maxD = len(slotNibs) - 1
	}
	kb := b.chgKeyScratch[:0]
	kb = append(kb, domain...)
	kb = append(kb, slotNibs[:maxD]...)
	b.chgKeyScratch = kb
	for d := 0; d <= maxD; d++ {
		if b.stoSched.e[d] <= 1 || n < start-start%b.stoSched.e[d] {
			continue
		}
		pk := kb[:len(domain)+d]
		bit := uint16(1) << slotNibs[d]
		if p, ok := b.stoDirty[d][string(pk)]; ok {
			*p |= bit
		} else {
			v := bit
			b.stoDirty[d][string(pk)] = &v
		}
	}
}
