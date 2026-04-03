// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec.go implements compact changeset encoding for the freezer.
// Account changesets: [count:2LE] + per entry: [addr:20][valLen:1][value]
// Storage changesets: address-grouped to avoid repeating addr+inc per slot.

package ethel

import (
	"encoding/binary"
	"fmt"
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

// DecodeAccountChanges decodes compact account changeset bytes.
// Returns list of (address, originalValue) pairs.
func DecodeAccountChanges(data []byte) (addrs [][20]byte, values [][]byte, err error) {
	if len(data) < 2 {
		return nil, nil, nil
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for i := 0; i < count; i++ {
		if pos+21 > len(data) {
			return nil, nil, fmt.Errorf("truncated account entry %d", i)
		}
		var addr [20]byte
		copy(addr[:], data[pos:pos+20])
		pos += 20
		valLen := int(data[pos])
		pos++
		if pos+valLen > len(data) {
			return nil, nil, fmt.Errorf("truncated account value %d", i)
		}
		addrs = append(addrs, addr)
		values = append(values, data[pos:pos+valLen])
		pos += valLen
	}
	return addrs, values, nil
}

// DecodeStorageChanges decodes address-grouped storage changeset bytes.
// Returns list of (compositeKey[54], originalValue) pairs.
func DecodeStorageChanges(data []byte) (keys [][]byte, values [][]byte, err error) {
	if len(data) < 2 {
		return nil, nil, nil
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for g := 0; g < addrCount; g++ {
		if pos+24 > len(data) { // addr(20)+inc(2)+slotCount(2)
			return nil, nil, fmt.Errorf("truncated addr group %d", g)
		}
		prefix := data[pos : pos+22] // addr(20)+inc(2)
		pos += 22
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+33 > len(data) { // key(32)+valLen(1)
				return nil, nil, fmt.Errorf("truncated slot %d in group %d", s, g)
			}
			compositeKey := make([]byte, 54)
			copy(compositeKey[:22], prefix)
			copy(compositeKey[22:], data[pos:pos+32])
			pos += 32
			valLen := int(data[pos])
			pos++
			if pos+valLen > len(data) {
				return nil, nil, fmt.Errorf("truncated slot value %d in group %d", s, g)
			}
			keys = append(keys, compositeKey)
			values = append(values, data[pos:pos+valLen])
			pos += valLen
		}
	}
	return keys, values, nil
}
