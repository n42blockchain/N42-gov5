// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// derivestages.go — the growth stages of one contract (derive-ns, part one).
//
// A contract's final depth D is sized for its final key count, which makes a
// fold unit U = keys / 16^D keys large. While the contract was smaller, the
// same unit size is reached at a shallower depth: depth g is enough as long
// as the keys ever born stay at or below U * 16^g. The rung to depth g is set
// at the block where the births cross U * 16^(g-1); the keys born before the
// last rung are copied into birth partitions (birthparts.go), and each early
// stage gets its own per-block records at level g-1, up to the next rung.
//
// All of it is a fraction of the contract's history: it ends where the
// contract had 1/16 of its final keys.
package datc

import (
	"bytes"
	"encoding/binary"
	"sort"
)

// stageBin is the block granularity of a rung. Births are counted per bin, so
// a rung sits at a bin's first block and the stage before it never exceeds
// its cap.
var stageBin uint64 = 1024 // a var for the tests' 360-block chains

func stoBlockOf(k []byte) uint64 {
	return uint64(binary.BigEndian.Uint32(k[stoDomainLen+32:]))
}

// contractRungs scans the contract's keys and returns the blocks at which its
// depth becomes 1..depth.
func contractRungs(set *leafSegSet, dom []byte, depth int) (bounds []uint64, keys uint64, err error) {
	births := map[uint64]uint64{}
	var last []byte
	c := set.Cursor()
	k, _, err := c.Seek(dom)
	for ; k != nil; k, _, err = c.Next() {
		if err != nil {
			return nil, 0, err
		}
		if !bytes.HasPrefix(k, dom) {
			break
		}
		if len(k) != stoDomainLen+32+blkLen {
			continue
		}
		if last == nil || !bytes.Equal(last, k[:stoDomainLen+32]) {
			last = append(last[:0], k[:stoDomainLen+32]...)
			births[stoBlockOf(k)/stageBin]++
			keys++
		}
	}
	if err != nil {
		return nil, 0, err
	}
	pow := uint64(1)
	for i := 0; i < depth; i++ {
		pow *= 16
	}
	unit := (keys + pow - 1) / pow
	bins := make([]uint64, 0, len(births))
	for b := range births {
		bins = append(bins, b)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i] < bins[j] })
	var cum uint64
	bi := 0
	capG := unit // depth g starts where the births pass unit * 16^(g-1)
	for g := 1; g <= depth; g++ {
		for bi < len(bins) && cum+births[bins[bi]] <= capG {
			cum += births[bins[bi]]
			bi++
		}
		at := uint64(0)
		if bi < len(bins) {
			at = bins[bi] * stageBin
		} else if len(bounds) > 0 {
			at = bounds[len(bounds)-1]
		}
		if len(bounds) > 0 && at < bounds[len(bounds)-1] {
			at = bounds[len(bounds)-1]
		}
		bounds = append(bounds, at)
		capG *= 16
	}
	return bounds, keys, nil
}

// deriveStages writes the contract's birth partitions and the records of its
// early stages, and returns, per early stage g (index g, 1 <= g < depth), the
// level g-1 nodes as of each sample.
func deriveStages(set *leafSegSet, c deriveContract, bounds []uint64, samples []deriveSample,
	emit func(table int, k, v []byte) error, st *deriveStats) ([][][]deriveNodeAt, error) {

	lastBound := bounds[len(bounds)-1]
	var early []deriveRow
	var rows []kvPair
	flush := func() error {
		if err := partitionRows(bounds, segTabLeafS0, rows, stoBlockOf, emit); err != nil {
			return err
		}
		for _, r := range rows {
			if stoBlockOf(r.k) >= lastBound {
				break
			}
			dr := deriveRow{block: uint32(stoBlockOf(r.k)), val: r.v}
			copy(dr.slot[:], r.k[stoDomainLen:])
			early = append(early, dr)
		}
		rows = rows[:0]
		return nil
	}
	cur := set.Cursor()
	k, v, err := cur.Seek(c.dom)
	for ; k != nil; k, v, err = cur.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, c.dom) {
			break
		}
		if len(k) != stoDomainLen+32+blkLen {
			continue
		}
		if len(rows) > 0 && !bytes.Equal(rows[0].k[:stoDomainLen+32], k[:stoDomainLen+32]) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		// The first row dates the key's birth; past the last rung nothing is
		// read from a partition or an early-stage record.
		if len(rows) == 0 || stoBlockOf(k) < lastBound {
			rows = append(rows, kvPair{k: append([]byte(nil), k...), v: append([]byte(nil), v...)})
		}
	}
	if err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}

	out := make([][][]deriveNodeAt, c.depth)
	emitNS := func(k, v []byte) error { return emit(segTabNodeS, k, v) }
	for g := 1; g < c.depth; g++ {
		level := g - 1
		nodes := 1
		for i := 0; i < level; i++ {
			nodes *= 16
		}
		byNode := make([][]deriveRow, nodes)
		for _, r := range early {
			if uint64(r.block) >= bounds[g] {
				continue
			}
			idx := 0
			for i := 0; i < level; i++ {
				idx = idx*16 + int(nibbleAt(r.slot[:], i))
			}
			byNode[idx] = append(byNode[idx], r)
		}
		out[g] = make([][]deriveNodeAt, nodes)
		for idx := range byNode {
			path := make([]byte, level)
			for i, x := level-1, idx; i >= 0; i, x = i-1, x/16 {
				path[i] = byte(x % 16)
			}
			at, err := deriveNode(byNode[idx], c.dom, path, samples, 0, bounds[g], emitNS, st)
			if err != nil {
				return nil, err
			}
			out[g][idx] = at
		}
	}
	return out, nil
}

func nibbleAt(b []byte, i int) byte {
	if i%2 == 0 {
		return b[i/2] >> 4
	}
	return b[i/2] & 0x0f
}
