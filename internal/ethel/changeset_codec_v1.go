// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec_v1.go implements the V1 wire format for ethexec
// account/storage changesets. V1 differs from V0 (changeset_codec.go) in
// two ways:
//
//  1. Addresses and codeHashes are dictionary-interned to 3-byte ids.
//     The dictionaries live in MDBX (AddrDict / CodeHashDict) — see
//     changeset_dict.go for details.
//  2. Account entries store only the fields that changed, plus their
//     old/new pair. Unchanged fields are skipped entirely. CodeHash is
//     never written when unchanged (saves 32B → 0B per contract call,
//     the dominant modification pattern).
//
// V1 wire format — account changeset blob, per block:
//
//	[count 2LE]
//	per entry:
//	  [addrID 3B BE]
//	  [flags 1B]
//	  payload (depends on flags; see encoder)
//
// flags layout:
//
//	bit 0 (0x01): old account exists
//	bit 1 (0x02): new account exists
//	bit 2 (0x04): nonce slot present in payload
//	bit 3 (0x08): balance slot present in payload
//	bit 4 (0x10): codeHash slot present in payload
//	bit 5..7    : reserved (must be 0; incarnation permanently dropped)
//
// Three cases dispatch on bits 0..1:
//
//	old && !new (delete/SELFDESTRUCT):
//	  if bit2: [oldNonce uvarint]
//	  if bit3: [oldBalLen 1B][oldBal: oldBalLen bytes]
//	  if bit4: [oldCodeHashID 3B BE]
//
//	!old && new (create):
//	  if bit2: [newNonce uvarint]
//	  if bit3: [newBalLen 1B][newBal]
//	  if bit4: [newCodeHashID 3B BE]
//
//	old && new (modify):
//	  if bit2: [oldNonce uvarint][newNonce uvarint]
//	  if bit3: [oldBalLen 1B][oldBal][newBalLen 1B][newBal]
//	  if bit4: [oldCodeHashID 3B BE][newCodeHashID 3B BE]
//
// V1 wire format — storage changeset blob, per block:
//
//	[addrCount 2LE]
//	per address group:
//	  [addrID 3B BE]
//	  [slotCount 2LE]
//	  per slot:
//	    [slot 32B]
//	    [oldLen 1B][oldVal]
//	    [newLen 1B][newVal]
//
// The storage layout is unchanged from V0 except the leading 20B address is
// replaced by a 3B id. Slot encoding is left alone — U256.Bytes() is
// already at the per-entry information-theoretic minimum.

package ethel

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

// V1 flag bits.
const (
	flagOldExists      byte = 0x01
	flagNewExists      byte = 0x02
	flagNonceField     byte = 0x04
	flagBalanceField   byte = 0x08
	flagCodeHashField  byte = 0x10
	flagReservedMask   byte = 0xE0 // bits 5,6,7 must be zero on read
)

// EncodeAccountChangesV1 encodes the per-block account changeset using
// V1 dictionary + sparse-delta format. The newValueOf callback returns the
// post-block V2-encoded account bytes for an address; nil / empty result
// means the account was deleted.
//
// The dict writer is consulted for every address and every non-empty
// codeHash. Returns an error if the dictionary is full or any MDBX put
// fails.
func EncodeAccountChangesV1(
	cs *changeset.ChangeSet,
	newValueOf AccountNewValueFn,
	dict DictInterner,
) ([]byte, error) {
	if cs == nil || cs.Len() == 0 {
		return nil, nil
	}
	if dict == nil {
		return nil, fmt.Errorf("EncodeAccountChangesV1: dict is nil")
	}
	sort.Sort(cs)

	if cs.Len() > 0xFFFF {
		return nil, fmt.Errorf("EncodeAccountChangesV1: count %d > uint16 max", cs.Len())
	}

	buf := make([]byte, 0, 2+cs.Len()*16)
	buf = appendUint16LE(buf, uint16(cs.Len()))

	for _, c := range cs.Changes {
		if len(c.Key) < 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], c.Key[:20])

		addrID, err := dict.InternAddr(addr)
		if err != nil {
			return nil, fmt.Errorf("intern addr %x: %w", addr, err)
		}

		oldAcc, err := decodeOldAccount(c.Value)
		if err != nil {
			return nil, fmt.Errorf("decode old account %x: %w", addr, err)
		}

		var newBytes []byte
		if newValueOf != nil {
			newBytes = newValueOf(addr)
		}
		newAcc, err := decodeNewAccount(newBytes)
		if err != nil {
			return nil, fmt.Errorf("decode new account %x: %w", addr, err)
		}

		entry, err := encodeAccountEntryV1(addrID, oldAcc, newAcc, dict)
		if err != nil {
			return nil, fmt.Errorf("encode entry %x: %w", addr, err)
		}
		buf = append(buf, entry...)
	}
	return buf, nil
}

// encodeAccountEntryV1 builds a single per-account entry: addrID + flags +
// payload. Caller is responsible for passing already-interned addrID.
func encodeAccountEntryV1(
	addrID uint32,
	oldAcc, newAcc *account.StateAccount,
	dict DictInterner,
) ([]byte, error) {
	if oldAcc == nil && newAcc == nil {
		return nil, fmt.Errorf("entry has neither old nor new")
	}

	var idBuf [3]byte
	putBE24(idBuf[:], addrID)

	flags := byte(0)
	if oldAcc != nil {
		flags |= flagOldExists
	}
	if newAcc != nil {
		flags |= flagNewExists
	}

	var payload []byte
	switch {
	case oldAcc != nil && newAcc == nil:
		// Delete: encode old fields that are non-default.
		if oldAcc.Nonce > 0 {
			flags |= flagNonceField
		}
		if !oldAcc.Balance.IsZero() {
			flags |= flagBalanceField
		}
		if !oldAcc.IsEmptyCodeHash() {
			flags |= flagCodeHashField
		}
		if flags&flagNonceField != 0 {
			payload = appendUvarint(payload, oldAcc.Nonce)
		}
		if flags&flagBalanceField != 0 {
			payload = appendBalance(payload, &oldAcc.Balance)
		}
		if flags&flagCodeHashField != 0 {
			id, err := dict.InternCodeHash(oldAcc.CodeHash)
			if err != nil {
				return nil, err
			}
			payload = appendBE24(payload, id)
		}

	case oldAcc == nil && newAcc != nil:
		// Create: encode new fields that are non-default.
		if newAcc.Nonce > 0 {
			flags |= flagNonceField
		}
		if !newAcc.Balance.IsZero() {
			flags |= flagBalanceField
		}
		if !newAcc.IsEmptyCodeHash() {
			flags |= flagCodeHashField
		}
		if flags&flagNonceField != 0 {
			payload = appendUvarint(payload, newAcc.Nonce)
		}
		if flags&flagBalanceField != 0 {
			payload = appendBalance(payload, &newAcc.Balance)
		}
		if flags&flagCodeHashField != 0 {
			id, err := dict.InternCodeHash(newAcc.CodeHash)
			if err != nil {
				return nil, err
			}
			payload = appendBE24(payload, id)
		}

	default:
		// Modify: only encode fields that actually differ.
		if oldAcc.Nonce != newAcc.Nonce {
			flags |= flagNonceField
		}
		if oldAcc.Balance.Cmp(&newAcc.Balance) != 0 {
			flags |= flagBalanceField
		}
		if oldAcc.CodeHash != newAcc.CodeHash {
			flags |= flagCodeHashField
		}
		if flags&flagNonceField != 0 {
			payload = appendUvarint(payload, oldAcc.Nonce)
			payload = appendUvarint(payload, newAcc.Nonce)
		}
		if flags&flagBalanceField != 0 {
			payload = appendBalance(payload, &oldAcc.Balance)
			payload = appendBalance(payload, &newAcc.Balance)
		}
		if flags&flagCodeHashField != 0 {
			oldID, err := dict.InternCodeHash(oldAcc.CodeHash)
			if err != nil {
				return nil, err
			}
			newID, err := dict.InternCodeHash(newAcc.CodeHash)
			if err != nil {
				return nil, err
			}
			payload = appendBE24(payload, oldID)
			payload = appendBE24(payload, newID)
		}
	}

	out := make([]byte, 0, 4+len(payload))
	out = append(out, idBuf[:]...)
	out = append(out, flags)
	out = append(out, payload...)
	return out, nil
}

// EncodeStorageChangesV1 mirrors V0 storage encoding but replaces the 20B
// address prefix in each address group with a 3B dictionary id. Slot keys
// and U256 values are left raw.
func EncodeStorageChangesV1(
	cs *changeset.ChangeSet,
	newValueOf StorageNewValueFn,
	dict DictInterner,
) ([]byte, error) {
	if cs == nil || cs.Len() == 0 {
		return nil, nil
	}
	if dict == nil {
		return nil, fmt.Errorf("EncodeStorageChangesV1: dict is nil")
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

	// Same chunking rule as V0: slotCount is uint16, so split addr groups
	// of >65535 slots. Allocate fresh backing array to avoid the aliasing
	// bug that fixed in V0 (see changeset_codec.go:154).
	const maxGroupSlots = 65535
	needsChunk := false
	for _, g := range groups {
		if len(g.slots) > maxGroupSlots {
			needsChunk = true
			break
		}
	}
	if needsChunk {
		chunked := make([]addrGroup, 0, len(groups)+2)
		for _, g := range groups {
			if len(g.slots) <= maxGroupSlots {
				chunked = append(chunked, g)
				continue
			}
			for start := 0; start < len(g.slots); start += maxGroupSlots {
				end := start + maxGroupSlots
				if end > len(g.slots) {
					end = len(g.slots)
				}
				chunked = append(chunked, addrGroup{addr: g.addr, slots: g.slots[start:end]})
			}
		}
		groups = chunked
	}

	if len(groups) > 0xFFFF {
		return nil, fmt.Errorf("EncodeStorageChangesV1: addrCount %d > uint16 max", len(groups))
	}

	buf := make([]byte, 0, 2+len(groups)*8+cs.Len()*68)
	buf = appendUint16LE(buf, uint16(len(groups)))
	for _, g := range groups {
		addrID, err := dict.InternAddr(g.addr)
		if err != nil {
			return nil, fmt.Errorf("intern addr %x: %w", g.addr, err)
		}
		buf = appendBE24(buf, addrID)

		if len(g.slots) > maxGroupSlots {
			return nil, fmt.Errorf("EncodeStorageChangesV1: slotCount %d > uint16 (addr=%x)",
				len(g.slots), g.addr)
		}
		buf = appendUint16LE(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			if len(s.old) > maxStorageValueLen {
				return nil, fmt.Errorf("EncodeStorageChangesV1: oldVal %d bytes > %d (addr=%x slot=%x)",
					len(s.old), maxStorageValueLen, g.addr, s.key)
			}
			buf = append(buf, s.key...)
			buf = append(buf, byte(len(s.old)))
			buf = append(buf, s.old...)

			var newVal []byte
			if newValueOf != nil {
				var slot types.Hash
				copy(slot[:], s.key)
				newVal = newValueOf(g.addr, slot)
			}
			if len(newVal) > maxStorageValueLen {
				return nil, fmt.Errorf("EncodeStorageChangesV1: newVal %d bytes > %d (addr=%x slot=%x)",
					len(newVal), maxStorageValueLen, g.addr, s.key)
			}
			buf = append(buf, byte(len(newVal)))
			buf = append(buf, newVal...)
		}
	}
	return buf, nil
}

// DecodeAccountChangesV1 parses a V1-encoded account changeset blob,
// resolving addrIDs and codeHashIDs through the dict reader. Returns the
// same []AccountChange shape as DecodeAccountChanges but with full V2
// account bytes reconstructed for both old and new sides.
func DecodeAccountChangesV1(data []byte, dict *DictReader) ([]AccountChange, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if dict == nil {
		return nil, fmt.Errorf("DecodeAccountChangesV1: dict is nil")
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("account changeset V1: truncated header")
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	out := make([]AccountChange, 0, count)

	for i := 0; i < count; i++ {
		if pos+4 > len(data) {
			return nil, fmt.Errorf("account changeset V1: truncated entry %d header", i)
		}
		addrID := readBE24(data[pos : pos+3])
		pos += 3
		flags := data[pos]
		pos++

		if flags&flagReservedMask != 0 {
			return nil, fmt.Errorf("account changeset V1: entry %d reserved flag bits set (flags=0x%02x)", i, flags)
		}
		oldExists := flags&flagOldExists != 0
		newExists := flags&flagNewExists != 0
		if !oldExists && !newExists {
			return nil, fmt.Errorf("account changeset V1: entry %d has neither old nor new", i)
		}
		hasNonce := flags&flagNonceField != 0
		hasBal := flags&flagBalanceField != 0
		hasCH := flags&flagCodeHashField != 0

		addr, err := dict.LookupAddr(addrID)
		if err != nil {
			return nil, fmt.Errorf("entry %d addrID=%d: %w", i, addrID, err)
		}

		var oldAcc, newAcc *account.StateAccount

		switch {
		case oldExists && !newExists:
			oldAcc = newEmptyAccount() // empty defaults
			if hasNonce {
				v, n := binary.Uvarint(data[pos:])
				if n <= 0 {
					return nil, fmt.Errorf("entry %d: malformed oldNonce", i)
				}
				oldAcc.Nonce = v
				pos += n
			}
			if hasBal {
				if pos >= len(data) {
					return nil, fmt.Errorf("entry %d: truncated oldBalLen", i)
				}
				balLen := int(data[pos])
				pos++
				if balLen > 32 || pos+balLen > len(data) {
					return nil, fmt.Errorf("entry %d: bad oldBalLen=%d", i, balLen)
				}
				setBalanceFromBytes(&oldAcc.Balance, data[pos:pos+balLen])
				pos += balLen
			}
			if hasCH {
				if pos+3 > len(data) {
					return nil, fmt.Errorf("entry %d: truncated oldCodeHashID", i)
				}
				id := readBE24(data[pos : pos+3])
				pos += 3
				h, err := dict.LookupCodeHash(id)
				if err != nil {
					return nil, fmt.Errorf("entry %d oldCodeHashID=%d: %w", i, id, err)
				}
				oldAcc.CodeHash = h
			}
			oldAcc.Initialised = true

		case !oldExists && newExists:
			newAcc = newEmptyAccount()
			if hasNonce {
				v, n := binary.Uvarint(data[pos:])
				if n <= 0 {
					return nil, fmt.Errorf("entry %d: malformed newNonce", i)
				}
				newAcc.Nonce = v
				pos += n
			}
			if hasBal {
				if pos >= len(data) {
					return nil, fmt.Errorf("entry %d: truncated newBalLen", i)
				}
				balLen := int(data[pos])
				pos++
				if balLen > 32 || pos+balLen > len(data) {
					return nil, fmt.Errorf("entry %d: bad newBalLen=%d", i, balLen)
				}
				setBalanceFromBytes(&newAcc.Balance, data[pos:pos+balLen])
				pos += balLen
			}
			if hasCH {
				if pos+3 > len(data) {
					return nil, fmt.Errorf("entry %d: truncated newCodeHashID", i)
				}
				id := readBE24(data[pos : pos+3])
				pos += 3
				h, err := dict.LookupCodeHash(id)
				if err != nil {
					return nil, fmt.Errorf("entry %d newCodeHashID=%d: %w", i, id, err)
				}
				newAcc.CodeHash = h
			}
			newAcc.Initialised = true

		default:
			// Modify: each present field carries old + new pair.
			oldAcc = newEmptyAccount()
			newAcc = newEmptyAccount()
			if hasNonce {
				v, n := binary.Uvarint(data[pos:])
				if n <= 0 {
					return nil, fmt.Errorf("entry %d: malformed oldNonce(modify)", i)
				}
				oldAcc.Nonce = v
				pos += n
				v, n = binary.Uvarint(data[pos:])
				if n <= 0 {
					return nil, fmt.Errorf("entry %d: malformed newNonce(modify)", i)
				}
				newAcc.Nonce = v
				pos += n
			}
			if hasBal {
				if pos >= len(data) {
					return nil, fmt.Errorf("entry %d: truncated oldBalLen(modify)", i)
				}
				oldBalLen := int(data[pos])
				pos++
				if oldBalLen > 32 || pos+oldBalLen > len(data) {
					return nil, fmt.Errorf("entry %d: bad oldBalLen=%d(modify)", i, oldBalLen)
				}
				setBalanceFromBytes(&oldAcc.Balance, data[pos:pos+oldBalLen])
				pos += oldBalLen

				if pos >= len(data) {
					return nil, fmt.Errorf("entry %d: truncated newBalLen(modify)", i)
				}
				newBalLen := int(data[pos])
				pos++
				if newBalLen > 32 || pos+newBalLen > len(data) {
					return nil, fmt.Errorf("entry %d: bad newBalLen=%d(modify)", i, newBalLen)
				}
				setBalanceFromBytes(&newAcc.Balance, data[pos:pos+newBalLen])
				pos += newBalLen
			}
			if hasCH {
				if pos+6 > len(data) {
					return nil, fmt.Errorf("entry %d: truncated codeHashID pair", i)
				}
				oldID := readBE24(data[pos : pos+3])
				pos += 3
				newID := readBE24(data[pos : pos+3])
				pos += 3
				oh, err := dict.LookupCodeHash(oldID)
				if err != nil {
					return nil, fmt.Errorf("entry %d oldCodeHashID=%d: %w", i, oldID, err)
				}
				nh, err := dict.LookupCodeHash(newID)
				if err != nil {
					return nil, fmt.Errorf("entry %d newCodeHashID=%d: %w", i, newID, err)
				}
				oldAcc.CodeHash = oh
				newAcc.CodeHash = nh
			}
			oldAcc.Initialised = true
			newAcc.Initialised = true
		}

		change := AccountChange{Address: addr}
		if oldAcc != nil {
			change.OldValue = oldAcc.MarshalV2()
		}
		if newAcc != nil {
			change.NewValue = newAcc.MarshalV2()
		}
		out = append(out, change)
	}
	return out, nil
}

// DecodeStorageChangesV1 parses a V1 storage changeset blob, resolving the
// 3B address ids back to full 20B addresses via the dict reader. The
// returned shape matches DecodeStorageChanges (V0).
func DecodeStorageChangesV1(data []byte, dict *DictReader) ([]StorageChange, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if dict == nil {
		return nil, fmt.Errorf("DecodeStorageChangesV1: dict is nil")
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("storage changeset V1: truncated header")
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	var out []StorageChange

	for g := 0; g < addrCount; g++ {
		if pos+5 > len(data) {
			return nil, fmt.Errorf("storage changeset V1: truncated addr group %d", g)
		}
		addrID := readBE24(data[pos : pos+3])
		pos += 3
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2

		addr, err := dict.LookupAddr(addrID)
		if err != nil {
			return nil, fmt.Errorf("group %d addrID=%d: %w", g, addrID, err)
		}

		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				return nil, fmt.Errorf("storage changeset V1: truncated slot %d in group %d", s, g)
			}
			compositeKey := make([]byte, 52)
			copy(compositeKey[:20], addr[:])
			copy(compositeKey[20:], data[pos:pos+32])
			pos += 32

			oldLen := int(data[pos])
			pos++
			if oldLen > maxStorageValueLen {
				return nil, fmt.Errorf("storage changeset V1: oldLen %d > %d at g=%d s=%d",
					oldLen, maxStorageValueLen, g, s)
			}
			if pos+oldLen+1 > len(data) {
				return nil, fmt.Errorf("storage changeset V1: truncated old value")
			}
			oldVal := copyBytes(data[pos : pos+oldLen])
			pos += oldLen

			newLen := int(data[pos])
			pos++
			if newLen > maxStorageValueLen {
				return nil, fmt.Errorf("storage changeset V1: newLen %d > %d at g=%d s=%d",
					newLen, maxStorageValueLen, g, s)
			}
			if pos+newLen > len(data) {
				return nil, fmt.Errorf("storage changeset V1: truncated new value")
			}
			newVal := copyBytes(data[pos : pos+newLen])
			pos += newLen

			out = append(out, StorageChange{
				CompositeKey: compositeKey,
				OldValue:     oldVal,
				NewValue:     newVal,
			})
		}
	}
	return out, nil
}

// ---- helpers ----

func newEmptyAccount() *account.StateAccount {
	a := account.NewAccount()
	return &a
}

func decodeOldAccount(b []byte) (*account.StateAccount, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorageV2(b); err != nil {
		return nil, err
	}
	a.Initialised = true
	return &a, nil
}

func decodeNewAccount(b []byte) (*account.StateAccount, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorageV2(b); err != nil {
		return nil, err
	}
	a.Initialised = true
	return &a, nil
}

func appendUvarint(buf []byte, v uint64) []byte {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}

func appendBalance(buf []byte, bal *uint256.Int) []byte {
	bb := bal.Bytes32()
	start := 0
	for start < 32 && bb[start] == 0 {
		start++
	}
	trim := bb[start:]
	buf = append(buf, byte(len(trim)))
	buf = append(buf, trim...)
	return buf
}

func appendBE24(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>16), byte(v>>8), byte(v))
}

func setBalanceFromBytes(dst *uint256.Int, b []byte) {
	if len(b) == 0 {
		dst.Clear()
		return
	}
	var pad [32]byte
	copy(pad[32-len(b):], b)
	dst.SetBytes32(pad[:])
}
