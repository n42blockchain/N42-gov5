// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// The portable QMDB 0x03 (tombstone) snapshot of §1.5, as lib/qmdb writes it
// (WritePortableSnapshotTombstone): the state-sync payload is that format
// paged by twig. Servers derive their twigs from it; clients reassemble one
// from verified twigs and hand it to the existing importer.

var portableMagic03 = [8]byte{'N', '4', '2', 'Q', 'M', 'D', 'B', 3}

// Portable record kinds (lib/qmdb PortableKind*).
const (
	portableKindDead     byte = 0
	portableKindLive     byte = 1
	portableKindTomb     byte = 2
	portableKindDeadTwig byte = 3
)

// PortableHeader is the 0x03 header.
type PortableHeader struct {
	ChainID     uint64
	GenesisHash Hash
	BlockNumber uint64
	BlockHash   Hash
	Root        Hash
	NextSlot    uint64
}

// ServedTwig is one twig of a pivot in wire form.
type ServedTwig struct {
	ID      uint64
	Bits    [TwigBitsLen]byte
	Records []Record // from the twig's first slot; LIVE records carry KeyHash
	Roots   TwigRoots
}

// TwigsFromPortable parses a 0x03 snapshot into its twigs and checks that
// they fold to the header root.
func TwigsFromPortable(encoded []byte) (PortableHeader, []ServedTwig, error) {
	var h PortableHeader
	if len(encoded) < 144+32 {
		return h, nil, errors.New("syncwire: portable snapshot truncated")
	}
	payload, digest := encoded[:len(encoded)-32], encoded[len(encoded)-32:]
	if sum := blake3.Sum256(payload); !bytes.Equal(sum[:], digest) {
		return h, nil, errors.New("syncwire: portable snapshot digest mismatch")
	}
	if !bytes.Equal(payload[:8], portableMagic03[:]) {
		return h, nil, errors.New("syncwire: not a 0x03 portable snapshot")
	}
	rd := reader{b: payload[8:]}
	h.ChainID, _ = rd.u64()
	_ = rd.hash(&h.GenesisHash)
	h.BlockNumber, _ = rd.u64()
	_ = rd.hash(&h.BlockHash)
	_ = rd.hash(&h.Root)
	h.NextSlot, _ = rd.u64()
	entries, _ := rd.u64()
	if _, err := rd.u64(); err != nil { // tombstones
		return h, nil, err
	}
	if entries != h.NextSlot {
		return h, nil, fmt.Errorf("syncwire: entry_count %d != next_slot %d", entries, h.NextSlot)
	}
	twigCount := (h.NextSlot + TwigSize - 1) / TwigSize
	twigs := make([]ServedTwig, twigCount)
	for i := range twigs {
		twigs[i].ID = uint64(i)
	}
	leaves := make([]Hash, TwigSize)
	slot := uint64(0)
	for slot < h.NextSlot {
		s, err := rd.u64()
		if err != nil {
			return h, nil, err
		}
		if s != slot {
			return h, nil, fmt.Errorf("syncwire: record for slot %d, expected %d", s, slot)
		}
		kind, err := rd.u8()
		if err != nil {
			return h, nil, err
		}
		tw := &twigs[slot/TwigSize]
		local := slot % TwigSize
		switch kind {
		case portableKindLive, portableKindDead:
			var r Record
			if err := rd.hash(&r.KeyHash); err != nil {
				return h, nil, err
			}
			if r.Value, err = rd.value(); err != nil {
				return h, nil, err
			}
			if kind == portableKindLive {
				r.Kind = KindLive
				tw.Bits[local/8] |= 1 << (local % 8)
				leaves[local] = LeafHash(r.KeyHash, r.Value)
			} else {
				r = Record{Kind: KindDead, LeafHash: LeafHash(r.KeyHash, r.Value)}
				leaves[local] = r.LeafHash
			}
			tw.Records = append(tw.Records, r)
			slot++
		case portableKindTomb:
			var r Record
			r.Kind = KindDead
			if err := rd.hash(&r.LeafHash); err != nil {
				return h, nil, err
			}
			leaves[local] = r.LeafHash
			tw.Records = append(tw.Records, r)
			slot++
		case portableKindDeadTwig:
			n, err := rd.u16()
			if err != nil {
				return h, nil, err
			}
			if n != TwigSize || local != 0 || slot+TwigSize > h.NextSlot {
				return h, nil, fmt.Errorf("syncwire: dead-twig record at slot %d count %d", slot, n)
			}
			r := Record{Kind: KindHollow}
			if err := rd.hash(&r.LeafRoot); err != nil {
				return h, nil, err
			}
			tw.Records = []Record{r}
			tw.Roots.LeafRoot = r.LeafRoot
			slot += TwigSize
		default:
			return h, nil, fmt.Errorf("syncwire: portable record kind %d", kind)
		}
		// Close a twig when its last appended slot is read.
		if slot%TwigSize == 0 || slot == h.NextSlot {
			id := (slot - 1) / TwigSize
			tw := &twigs[id]
			if !(len(tw.Records) == 1 && tw.Records[0].Kind == KindHollow) {
				n := slot - id*TwigSize
				tw.Roots.LeafRoot = FoldTwigLeaves(leaves[:n])
			}
			tw.Roots.BitsRoot = BitsRoot(&tw.Bits)
			for i := range leaves {
				leaves[i] = Hash{}
			}
		}
	}
	if !rd.done() {
		return h, nil, fmt.Errorf("syncwire: %d bytes after the last record", len(rd.b))
	}
	roots := make([]Hash, len(twigs))
	for i := range twigs {
		roots[i] = TwigRoot(twigs[i].Roots)
	}
	if got := UpperRoot(roots); got != h.Root {
		return h, nil, fmt.Errorf("syncwire: twigs fold to %x, header root %x", got, h.Root)
	}
	return h, twigs, nil
}

// AssemblePortable writes the 0x03 snapshot of verified twigs (ascending,
// complete, each from its first slot). LIVE_P records become kind 1 (the
// portable file carries the keyHash only).
func AssemblePortable(h PortableHeader, twigs []ServedTwig) ([]byte, error) {
	var tombstones uint64
	var body []byte
	slot := uint64(0)
	for i, tw := range twigs {
		if tw.ID != uint64(i) {
			return nil, fmt.Errorf("syncwire: twig %d at position %d", tw.ID, i)
		}
		start, end := TwigSlots(tw.ID, h.NextSlot, 0)
		if len(tw.Records) == 1 && tw.Records[0].Kind == KindHollow {
			body = binary.LittleEndian.AppendUint64(body, start)
			body = append(body, portableKindDeadTwig)
			body = binary.LittleEndian.AppendUint16(body, TwigSize)
			body = append(body, tw.Records[0].LeafRoot[:]...)
			tombstones++
			slot = end
			continue
		}
		if uint64(len(tw.Records)) != end-start {
			return nil, fmt.Errorf("syncwire: twig %d has %d records for %d slots", tw.ID, len(tw.Records), end-start)
		}
		for _, r := range tw.Records {
			body = binary.LittleEndian.AppendUint64(body, slot)
			switch r.Kind {
			case KindLive, KindLiveP:
				body = append(body, portableKindLive)
				body = append(body, r.KeyHash[:]...)
				body = binary.LittleEndian.AppendUint32(body, uint32(len(r.Value)))
				body = append(body, r.Value...)
			case KindDead:
				body = append(body, portableKindTomb)
				body = append(body, r.LeafHash[:]...)
				tombstones++
			default:
				return nil, fmt.Errorf("syncwire: record kind %d inside twig %d", r.Kind, tw.ID)
			}
			slot++
		}
	}
	if slot != h.NextSlot {
		return nil, fmt.Errorf("syncwire: twigs cover %d slots, next_slot %d", slot, h.NextSlot)
	}
	out := append([]byte(nil), portableMagic03[:]...)
	out = binary.LittleEndian.AppendUint64(out, h.ChainID)
	out = append(out, h.GenesisHash[:]...)
	out = binary.LittleEndian.AppendUint64(out, h.BlockNumber)
	out = append(out, h.BlockHash[:]...)
	out = append(out, h.Root[:]...)
	out = binary.LittleEndian.AppendUint64(out, h.NextSlot)
	out = binary.LittleEndian.AppendUint64(out, h.NextSlot)
	out = binary.LittleEndian.AppendUint64(out, tombstones)
	out = append(out, body...)
	sum := blake3.Sum256(out)
	return append(out, sum[:]...), nil
}
