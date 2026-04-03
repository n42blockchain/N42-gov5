// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec.go implements compact changeset encoding for the freezer.
// Account changesets: [count:2LE] + per entry: [addr:20][valLen:1][value]
// Storage changesets: address-grouped to avoid repeating addr+inc per slot.

package ethel

import (
	"encoding/binary"
	"sort"

	"github.com/n42blockchain/N42/modules/changeset"
)

// EncodeAccountChanges encodes account changesets compactly.
// Format: [count:2LE] + per entry: [addr:20][valLen:1][value:0-N]
// Values are already Erigon V2 compact (bitflag + varint).
func EncodeAccountChanges(cs *changeset.ChangeSet) []byte {
	if cs == nil || cs.Len() == 0 {
		return nil
	}
	sort.Sort(cs)
	buf := make([]byte, 0, 2+cs.Len()*26)
	buf = appendUint16LE(buf, uint16(cs.Len()))
	for _, c := range cs.Changes {
		if len(c.Key) < 20 {
			continue
		}
		buf = append(buf, c.Key[:20]...) // address
		buf = append(buf, byte(len(c.Value)))
		buf = append(buf, c.Value...)
	}
	return buf
}

// EncodeStorageChanges encodes storage changesets grouped by address.
// Format:
//
//	[addrCount:2LE]
//	per address:
//	  [addr:20][incarnation:2BE][slotCount:2LE]
//	  per slot:
//	    [slotKey:32][valLen:1][value:0-32]
//
// Grouping avoids repeating the 22-byte addr+inc prefix per slot.
func EncodeStorageChanges(cs *changeset.ChangeSet) []byte {
	if cs == nil || cs.Len() == 0 {
		return nil
	}
	sort.Sort(cs)

	// Group by address+incarnation (first 22 bytes of key).
	type slotEntry struct {
		key   []byte // 32 bytes
		value []byte // 0-32 bytes
	}
	type addrGroup struct {
		prefix [22]byte // addr(20) + incarnation(2)
		slots  []slotEntry
	}

	var groups []addrGroup
	var cur *addrGroup
	for _, c := range cs.Changes {
		if len(c.Key) < 54 { // addr(20)+inc(2)+slot(32)
			continue
		}
		var prefix [22]byte
		copy(prefix[:], c.Key[:22])
		if cur == nil || cur.prefix != prefix {
			groups = append(groups, addrGroup{prefix: prefix})
			cur = &groups[len(groups)-1]
		}
		cur.slots = append(cur.slots, slotEntry{key: c.Key[22:54], value: c.Value})
	}

	buf := make([]byte, 0, 2+len(groups)*26+cs.Len()*36)
	buf = appendUint16LE(buf, uint16(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.prefix[:]...)           // addr(20)+inc(2)
		buf = appendUint16LE(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.key...)              // slotKey(32)
			buf = append(buf, byte(len(s.value)))    // valLen(1)
			buf = append(buf, s.value...)             // value(0-32)
		}
	}
	return buf
}

func appendUint16LE(buf []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(buf, tmp[:]...)
}
