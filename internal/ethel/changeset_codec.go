// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec.go implements the unified-changes V2 encoding for the
// account/storage changeset freezer tables. A single entry carries BOTH
// the block-origin (old) value and the post-block (new) value, so the
// triple {leaves, acctcs, storcs} collapses to {acctcs, storcs}. Forward
// replay reads new values, backward unwind reads old values — the whole
// pipeline can rebuild or tear down any state range without ever running
// the EVM, which is the driving requirement for this codec.
//
// Wire format, per-block blob:
//
//	[version:1]          // 0x01 for V2
//	[count:2LE]          // number of account / storage-address entries
//	per entry:
//	  ... see encoder ...
//
// The version byte lets future revisions be introduced without ambiguity.
// Legacy v0 data (no version prefix) must be migrated or re-emitted from
// a producer running the V2 codec.
//
// Value conventions (same for old and new):
//   - Account values are full V2-encoded StateAccount bytes
//     (account.MarshalV2) — NOT the omitHashes form used by the legacy
//     Erigon ChangeSet. This makes forward replay self-contained (the
//     PlainContractCode table does not need to be consulted).
//   - Storage slot values are raw big-endian minimal bytes (0-32 bytes,
//     leading zeros trimmed). A len=0 value means "slot is zero / absent".
//     On forward replay or backward unwind, len=0 means DELETE the key.

package ethel

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

const (
	// changesetVersionV2 is the version byte prefix for unified changesets.
	// Every blob produced by the V2 encoders starts with this byte.
	changesetVersionV2 byte = 0x01
)

// AccountNewValueFn resolves the post-block V2-encoded account bytes for
// an address, given the current (post-commit) buffered-state reader. A
// nil return means the account was deleted in this block and forward
// replay should `delete(Account, addr)`.
type AccountNewValueFn func(addr types.Address) []byte

// StorageNewValueFn resolves the post-block raw storage value for
// (addr, slot). Nil / zero-length return means the slot was zeroed /
// wiped in this block; forward replay `delete(Storage, compositeKey)`.
type StorageNewValueFn func(addr types.Address, slot types.Hash) []byte

// EncodeAccountChangesV2 encodes account changeset bytes with both old
// and new values inline. The ChangeSet carries the block-origin old
// values; newValueOf resolves the post-block value for each address.
//
// Per-entry layout:
//
//	[addr:20]
//	[oldLen:1][oldVal:oldLen]   // V2-encoded full account, empty => deleted
//	[newLen:1][newVal:newLen]   // V2-encoded full account, empty => deleted
func EncodeAccountChangesV2(cs *changeset.ChangeSet, newValueOf AccountNewValueFn) []byte {
	if cs == nil || cs.Len() == 0 {
		return nil
	}
	sort.Sort(cs)
	// Re-encode old values using the FULL V2 form (not omitHashes) so the
	// blob is self-contained. The ChangeSet stored in cs was written by
	// ChangeSetWriter with omitHashes=true; we must restore codeHash here
	// before writing to the V2 changeset — but since the block-origin
	// codeHash is NOT visible from post-block PlainContractCode alone for
	// deleted contracts, we keep the omitHashes bytes as-is for old and
	// rely on PlainContractCode for backward-unwind recovery. The `new`
	// side, on the other hand, is always sourced from a full MarshalV2.
	buf := make([]byte, 0, 3+cs.Len()*60)
	buf = append(buf, changesetVersionV2)
	buf = appendUint16LE(buf, uint16(cs.Len()))
	for _, c := range cs.Changes {
		if len(c.Key) < 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], c.Key[:20])
		buf = append(buf, addr[:]...)

		// Old value (omitHashes bytes straight from the ChangeSet).
		buf = append(buf, byte(len(c.Value)))
		buf = append(buf, c.Value...)

		// New value (full MarshalV2 from post-block reader; nil → deleted).
		var newVal []byte
		if newValueOf != nil {
			newVal = newValueOf(addr)
		}
		buf = append(buf, byte(len(newVal)))
		buf = append(buf, newVal...)
	}
	return buf
}

// EncodeStorageChangesV2 encodes storage changeset bytes with both old
// and new slot values inline, grouped by address.
//
// Per-block layout:
//
//	[version:1][addrCount:2LE]
//	per address:
//	  [addr:20][slotCount:2LE]
//	  per slot:
//	    [slotKey:32]
//	    [oldLen:1][oldVal:oldLen]   // 0-32 bytes, empty => slot was zero
//	    [newLen:1][newVal:newLen]   // 0-32 bytes, empty => slot now zero
func EncodeStorageChangesV2(cs *changeset.ChangeSet, newValueOf StorageNewValueFn) []byte {
	if cs == nil || cs.Len() == 0 {
		return nil
	}
	sort.Sort(cs)

	type slotEntry struct {
		key []byte // 32 bytes
		old []byte
	}
	type addrGroup struct {
		addr  types.Address
		slots []slotEntry
	}

	var groups []addrGroup
	var cur *addrGroup
	for _, c := range cs.Changes {
		if len(c.Key) < 52 {
			continue
		}
		var addr types.Address
		copy(addr[:], c.Key[:20])
		if cur == nil || cur.addr != addr {
			groups = append(groups, addrGroup{addr: addr})
			cur = &groups[len(groups)-1]
		}
		cur.slots = append(cur.slots, slotEntry{key: c.Key[20:52], old: c.Value})
	}

	buf := make([]byte, 0, 3+len(groups)*24+cs.Len()*68)
	buf = append(buf, changesetVersionV2)
	buf = appendUint16LE(buf, uint16(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.addr[:]...)
		buf = appendUint16LE(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.key...)
			buf = append(buf, byte(len(s.old)))
			buf = append(buf, s.old...)

			var newVal []byte
			if newValueOf != nil {
				var slot types.Hash
				copy(slot[:], s.key)
				newVal = newValueOf(g.addr, slot)
			}
			buf = append(buf, byte(len(newVal)))
			buf = append(buf, newVal...)
		}
	}
	return buf
}

func appendUint16LE(buf []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(buf, tmp[:]...)
}

// AccountChangeV2 is a decoded per-account V2 changeset entry.
type AccountChangeV2 struct {
	Address  types.Address
	OldValue []byte // omitHashes bytes or empty
	NewValue []byte // full MarshalV2 bytes or empty
}

// StorageChangeV2 is a decoded per-slot V2 changeset entry.
type StorageChangeV2 struct {
	CompositeKey []byte // 52 bytes: addr(20) || slot(32)
	OldValue     []byte // 0-32 bytes
	NewValue     []byte // 0-32 bytes
}

// DecodeAccountChangesV2 parses a V2 account changeset blob.
func DecodeAccountChangesV2(data []byte) ([]AccountChangeV2, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] != changesetVersionV2 {
		return nil, fmt.Errorf("account changeset: unknown version byte 0x%02x (expected 0x%02x)",
			data[0], changesetVersionV2)
	}
	if len(data) < 3 {
		return nil, fmt.Errorf("account changeset: truncated header")
	}
	count := int(binary.LittleEndian.Uint16(data[1:3]))
	pos := 3
	out := make([]AccountChangeV2, 0, count)
	for i := 0; i < count; i++ {
		if pos+22 > len(data) { // addr(20) + oldLen(1) + newLen(1) minimum
			return nil, fmt.Errorf("account changeset: truncated entry %d", i)
		}
		var addr types.Address
		copy(addr[:], data[pos:pos+20])
		pos += 20

		oldLen := int(data[pos])
		pos++
		if pos+oldLen+1 > len(data) {
			return nil, fmt.Errorf("account changeset: truncated old value %d", i)
		}
		oldVal := copyBytes(data[pos : pos+oldLen])
		pos += oldLen

		newLen := int(data[pos])
		pos++
		if pos+newLen > len(data) {
			return nil, fmt.Errorf("account changeset: truncated new value %d", i)
		}
		newVal := copyBytes(data[pos : pos+newLen])
		pos += newLen

		out = append(out, AccountChangeV2{Address: addr, OldValue: oldVal, NewValue: newVal})
	}
	return out, nil
}

// DecodeStorageChangesV2 parses a V2 storage changeset blob.
func DecodeStorageChangesV2(data []byte) ([]StorageChangeV2, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] != changesetVersionV2 {
		return nil, fmt.Errorf("storage changeset: unknown version byte 0x%02x (expected 0x%02x)",
			data[0], changesetVersionV2)
	}
	if len(data) < 3 {
		return nil, fmt.Errorf("storage changeset: truncated header")
	}
	addrCount := int(binary.LittleEndian.Uint16(data[1:3]))
	pos := 3
	var out []StorageChangeV2
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			return nil, fmt.Errorf("storage changeset: truncated addr group %d", g)
		}
		addrBytes := data[pos : pos+20]
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) { // slot(32) + oldLen(1) + newLen(1) minimum
				return nil, fmt.Errorf("storage changeset: truncated slot %d in group %d", s, g)
			}
			compositeKey := make([]byte, 52)
			copy(compositeKey[:20], addrBytes)
			copy(compositeKey[20:], data[pos:pos+32])
			pos += 32

			oldLen := int(data[pos])
			pos++
			if pos+oldLen+1 > len(data) {
				return nil, fmt.Errorf("storage changeset: truncated slot old value")
			}
			oldVal := copyBytes(data[pos : pos+oldLen])
			pos += oldLen

			newLen := int(data[pos])
			pos++
			if pos+newLen > len(data) {
				return nil, fmt.Errorf("storage changeset: truncated slot new value")
			}
			newVal := copyBytes(data[pos : pos+newLen])
			pos += newLen

			out = append(out, StorageChangeV2{
				CompositeKey: compositeKey,
				OldValue:     oldVal,
				NewValue:     newVal,
			})
		}
	}
	return out, nil
}

func copyBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// EncodeGenesisAccountsV2 walks the Account table and emits a V2 account
// changeset blob with oldVal=empty and newVal=full encoding for every
// account. Used for block 0 so the changes stream is self-contained and
// forward replay from genesis needs no external allocation file.
func EncodeGenesisAccountsV2(iter AccountIterator) ([]byte, error) {
	buf := make([]byte, 0, 256*1024)
	buf = append(buf, changesetVersionV2)
	countPos := len(buf)
	buf = append(buf, 0, 0) // uint16 count placeholder

	count := 0
	err := iter(func(addr types.Address, v []byte) error {
		if len(v) > 255 {
			return fmt.Errorf("genesis account value too long: %d bytes", len(v))
		}
		buf = append(buf, addr[:]...)
		buf = append(buf, 0) // oldLen = 0 (no pre-genesis state)
		buf = append(buf, byte(len(v)))
		buf = append(buf, v...)
		count++
		if count > 0xFFFF {
			return fmt.Errorf("genesis has %d accounts, exceeds uint16 count limit", count)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint16(buf[countPos:], uint16(count))
	return buf, nil
}

// EncodeGenesisStoragesV2 walks the Storage table and emits a V2 storage
// changeset blob with oldVal=empty and newVal=current for every slot,
// grouped by address. The iterator must yield slots in ascending
// (address, slot) order so the address-grouped framing is correct.
func EncodeGenesisStoragesV2(iter StorageIterator) ([]byte, error) {
	type slotEntry struct {
		slot  types.Hash
		value []byte
	}
	type addrGroup struct {
		addr  types.Address
		slots []slotEntry
	}

	var groups []addrGroup
	var lastAddr types.Address
	var cur *addrGroup

	err := iter(func(addr types.Address, slot types.Hash, v []byte) error {
		if len(v) > 255 {
			return fmt.Errorf("genesis storage value too long: %d bytes", len(v))
		}
		if cur == nil || addr != lastAddr {
			lastAddr = addr
			groups = append(groups, addrGroup{addr: addr})
			cur = &groups[len(groups)-1]
		}
		cur.slots = append(cur.slots, slotEntry{slot: slot, value: copyBytes(v)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, nil
	}
	if len(groups) > 0xFFFF {
		return nil, fmt.Errorf("genesis has %d storage groups, exceeds uint16 limit", len(groups))
	}

	buf := make([]byte, 0, 64*1024)
	buf = append(buf, changesetVersionV2)
	buf = appendUint16LE(buf, uint16(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.addr[:]...)
		buf = appendUint16LE(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.slot[:]...)
			buf = append(buf, 0) // oldLen = 0
			buf = append(buf, byte(len(s.value)))
			buf = append(buf, s.value...)
		}
	}
	return buf, nil
}

// AccountIterator yields (address, v2-encoded bytes) pairs in address-sorted
// order. Used by genesis encoder to walk the initial PlainState.
type AccountIterator func(fn func(addr types.Address, v []byte) error) error

// StorageIterator yields (address, slot, raw value) triples in
// (address, slot)-sorted order. Used by genesis encoder.
type StorageIterator func(fn func(addr types.Address, slot types.Hash, v []byte) error) error

// Compile-time guard: the account package is referenced so forward
// replay callers can decode V2-encoded values without an extra import
// shim; this keeps the symbol reachable even when the decoder isn't
// exercised directly.
var _ = (*account.StateAccount)(nil)
