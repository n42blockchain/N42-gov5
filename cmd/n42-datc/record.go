// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// record.go — the DATC on-disk record formats, a build/verify shared contract.
//
// Node records (DatcAccNode/DatcStorNode): value = empty (tombstone) | flags
// byte (FULL | DIFF) followed by the encoding below. Change rows
// (DatcAccChg/DatcStorChg): packed varint(Δblock) + childNibble events. These
// encoders are the build side; verify.go decodes the exact same bytes. Keep
// the format here so neither side can drift.

package main

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/trie"
)

// kvPair is one pending (key, value) sorted-batch write.
type kvPair struct{ k, v []byte }

// chgEvent is one change event inside an aggregated row.
type chgEvent struct {
	block  uint32
	nibble byte
}

// chgEventSize is the in-slice size of a chgEvent (uint32 + byte, padded to the
// uint32 alignment) — used to estimate chgStoAgg heap for the mid-batch drain.
const chgEventSize = 8

// chgSlot is one account-side aggregation slot (flat-indexed by path).
type chgSlot struct {
	epoch  uint32
	events []chgEvent
}

const (
	bufFlushThreshold = 4_000_000 // entries per buffer before an early sorted flush

	// Node record value flags (empty value = tombstone, as before).
	nodeRecFull = 0x01 // flags | MarshalTrieNode bytes
	nodeRecDiff = 0x00 // flags | newMasks(6) | changedMask(2) | 32B × (changed ∧ newHasHash)

	// fullEvery bounds the reader's diff walk-back: at least every F-th epoch
	// (per path) the record is FULL.
	fullEvery = 8
)

// nibblesOf expands bytes to one-nibble-per-byte (erigon keyHex form, no terminator).
func nibblesOf(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = x >> 4
		out[2*i+1] = x & 0x0f
	}
	return out
}

func bytesEqualPrefix(k, p []byte) bool {
	return len(k) >= len(p) && string(k[:len(p)]) == string(p)
}

// encodeNodeDiff builds a DIFF record from the current node bytes and the
// epoch's changed-children bitmap. Only changed children's hashes are stored;
// the reader fills unchanged ones from the previous record chain.
func encodeNodeDiff(node []byte, changed uint16) []byte {
	hasState, hasTree, hasHash, hashes, _ := trie.UnmarshalTrieNode(node)
	out := make([]byte, 0, 9+32*4)
	out = append(out, nodeRecDiff)
	out = binary.BigEndian.AppendUint16(out, hasState)
	out = binary.BigEndian.AppendUint16(out, hasTree)
	out = binary.BigEndian.AppendUint16(out, hasHash)
	out = binary.BigEndian.AppendUint16(out, changed)
	hi := 0
	for nib := 0; nib < 16; nib++ {
		bit := uint16(1) << nib
		if hasHash&bit == 0 {
			continue
		}
		if changed&bit != 0 {
			out = append(out, hashes[hi*32:hi*32+32]...)
		}
		hi++
	}
	return out
}

// encodeChgRow packs an event list (ascending block) into one aggregated row:
// per event varint(Δblock from previous event) + nibble.
func encodeChgRow(events []chgEvent) []byte {
	out := make([]byte, 0, len(events)*3)
	prev := uint32(0)
	for i, e := range events {
		d := e.block - prev
		if i == 0 {
			d = e.block // first event: absolute-within-epoch via block number
		}
		out = binary.AppendUvarint(out, uint64(d))
		out = append(out, e.nibble)
		prev = e.block
	}
	return out
}
