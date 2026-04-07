// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/changeset"
)

// EncodeGenesisJournal builds a leaves journal containing ALL accounts and
// storage in the current PlainState. Used for block 0 so that the journal
// is self-contained (replaying from journal[0] rebuilds full state without
// needing a separate genesis.json).
func EncodeGenesisJournal(tx kv.Tx) ([]byte, error) {
	buf := make([]byte, 0, 256*1024) // 8K+ accounts

	// --- Accounts ---
	countPos := len(buf)
	buf = binary.LittleEndian.AppendUint32(buf, 0)
	numAcc := uint32(0)

	acctCursor, err := tx.Cursor("Account")
	if err != nil {
		return nil, fmt.Errorf("open Account cursor: %w", err)
	}
	defer acctCursor.Close()

	for k, v, err := acctCursor.First(); k != nil; k, v, err = acctCursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) != 20 {
			continue
		}
		numAcc++
		buf = append(buf, k...)
		if len(v) > 255 {
			return nil, fmt.Errorf("account value too long: %d bytes", len(v))
		}
		buf = append(buf, byte(len(v)))
		buf = append(buf, v...)
	}
	binary.LittleEndian.PutUint32(buf[countPos:], numAcc)

	// --- Storage ---
	type slotEntry struct {
		slot  [32]byte
		value []byte
	}
	type addrGroup struct {
		addr  [20]byte
		slots []slotEntry
	}

	var groups []addrGroup
	var lastAddr [20]byte
	var cur *addrGroup

	stoCursor, err := tx.Cursor("Storage")
	if err != nil {
		return nil, fmt.Errorf("open Storage cursor: %w", err)
	}
	defer stoCursor.Close()

	for k, v, err := stoCursor.First(); k != nil; k, v, err = stoCursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 52 {
			continue
		}
		var thisAddr [20]byte
		copy(thisAddr[:], k[:20])
		if cur == nil || thisAddr != lastAddr {
			lastAddr = thisAddr
			groups = append(groups, addrGroup{addr: thisAddr})
			cur = &groups[len(groups)-1]
		}
		var slot [32]byte
		copy(slot[:], k[20:52])
		cur.slots = append(cur.slots, slotEntry{slot: slot, value: append([]byte{}, v...)})
	}

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.addr[:]...)
		buf = binary.BigEndian.AppendUint16(buf, 0) // incarnation=0, kept for format compat
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.slot[:]...)
			if len(s.value) > 255 {
				return nil, fmt.Errorf("storage value too long: %d bytes", len(s.value))
			}
			buf = append(buf, byte(len(s.value)))
			buf = append(buf, s.value...)
		}
	}

	return buf, nil
}

// EncodeLeavesJournal builds a plain-key leaves journal from changesets.
// Records the plain key + NEW value (post-execution) for every changed leaf.
// Can be replayed forward from genesis to reconstruct full PlainState.
// Plain keys can be hashed on-the-fly (crypto.Keccak256) for trie updates.
//
// Format:
//
//	[numAccounts:4LE]
//	per account: [plainAddr:20][valLen:1][newValue:N]  (valLen=0 means deleted)
//
//	[numAddrs:4LE]
//	per address:
//	  [plainAddr:20][incarnation:2BE][numSlots:2LE]
//	  per slot: [plainSlot:32][valLen:1][newValue:0-32]  (valLen=0 means deleted)
func EncodeLeavesJournal(
	accCS *changeset.ChangeSet,
	stoCS *changeset.ChangeSet,
	getAccount func(addr types.Address) *account.StateAccount,
	getStorage func(addr types.Address, key types.Hash) []byte,
) []byte {
	buf := make([]byte, 0, 4096)

	// --- Account leaves ---
	countPos := len(buf)
	buf = binary.LittleEndian.AppendUint32(buf, 0)
	numAcc := uint32(0)
	if accCS != nil {
		for _, c := range accCS.Changes {
			if len(c.Key) < 20 {
				continue
			}
			numAcc++
			buf = append(buf, c.Key[:20]...)

			var addr types.Address
			copy(addr[:], c.Key[:20])
			acc := getAccount(addr)
			if acc == nil {
				buf = append(buf, 0)
			} else {
				encoded := acc.MarshalV2()
				buf = append(buf, byte(len(encoded)))
				buf = append(buf, encoded...)
			}
		}
	}
	binary.LittleEndian.PutUint32(buf[countPos:], numAcc)

	// --- Storage leaves (address-grouped) ---
	type slotLeaf struct {
		plainSlot [32]byte
		value     []byte
	}
	type addrLeaves struct {
		plainAddr   [20]byte
		incarnation uint16
		slots       []slotLeaf
	}

	var groups []addrLeaves
	var lastAddr [20]byte
	var cur *addrLeaves

	if stoCS != nil {
		for _, c := range stoCS.Changes {
			if len(c.Key) < 54 {
				continue
			}
			var thisAddr [20]byte
			copy(thisAddr[:], c.Key[:20])

			if cur == nil || thisAddr != lastAddr {
				lastAddr = thisAddr
				inc := binary.BigEndian.Uint16(c.Key[20:22])
				groups = append(groups, addrLeaves{plainAddr: thisAddr, incarnation: inc})
				cur = &groups[len(groups)-1]
			}

			var slot [32]byte
			copy(slot[:], c.Key[22:54])

			var addr types.Address
			copy(addr[:], c.Key[:20])
			var slotKey types.Hash
			copy(slotKey[:], c.Key[22:54])
			val := getStorage(addr, slotKey)

			cur.slots = append(cur.slots, slotLeaf{plainSlot: slot, value: val})
		}
	}

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.plainAddr[:]...)
		buf = binary.BigEndian.AppendUint16(buf, g.incarnation)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.plainSlot[:]...)
			if s.value == nil {
				buf = append(buf, 0)
			} else {
				buf = append(buf, byte(len(s.value)))
				buf = append(buf, s.value...)
			}
		}
	}
	return buf
}

// AccountLeaf is a decoded account entry from a leaves journal.
type AccountLeaf struct {
	Address types.Address
	Value   []byte // V2-encoded; nil = deleted
}

// StorageLeaf is a decoded storage slot from a leaves journal.
type StorageLeaf struct {
	Address types.Address
	Slot    types.Hash
	Value   []byte // nil = deleted
}

// DecodeLeavesJournal decodes a plain-key leaves journal.
// Returns account leaves and storage leaves for PlainState replay or trie update.
func DecodeLeavesJournal(data []byte) ([]AccountLeaf, []StorageLeaf, error) {
	if len(data) < 8 {
		return nil, nil, fmt.Errorf("leaves journal too short: %d bytes", len(data))
	}
	pos := 0

	// --- Accounts ---
	numAcc := binary.LittleEndian.Uint32(data[pos:])
	pos += 4

	accounts := make([]AccountLeaf, 0, numAcc)
	for i := uint32(0); i < numAcc; i++ {
		if pos+20 > len(data) {
			return nil, nil, fmt.Errorf("truncated at account %d", i)
		}
		var leaf AccountLeaf
		copy(leaf.Address[:], data[pos:pos+20])
		pos += 20

		if pos >= len(data) {
			return nil, nil, fmt.Errorf("truncated at account %d valLen", i)
		}
		valLen := int(data[pos])
		pos++

		if valLen > 0 {
			if pos+valLen > len(data) {
				return nil, nil, fmt.Errorf("truncated at account %d value", i)
			}
			leaf.Value = make([]byte, valLen)
			copy(leaf.Value, data[pos:pos+valLen])
			pos += valLen
		}
		accounts = append(accounts, leaf)
	}

	// --- Storage ---
	if pos+4 > len(data) {
		return nil, nil, fmt.Errorf("truncated at storage header")
	}
	numAddrs := binary.LittleEndian.Uint32(data[pos:])
	pos += 4

	var storage []StorageLeaf
	for i := uint32(0); i < numAddrs; i++ {
		if pos+24 > len(data) {
			return nil, nil, fmt.Errorf("truncated at storage group %d", i)
		}
		var addr types.Address
		copy(addr[:], data[pos:pos+20])
		pos += 20

		pos += 2 // skip incarnation (2B, always 0, kept for format compat)

		numSlots := binary.LittleEndian.Uint16(data[pos:])
		pos += 2

		for j := uint16(0); j < numSlots; j++ {
			if pos+32 > len(data) {
				return nil, nil, fmt.Errorf("truncated at slot %d/%d", i, j)
			}
			var leaf StorageLeaf
			leaf.Address = addr
			copy(leaf.Slot[:], data[pos:pos+32])
			pos += 32

			if pos >= len(data) {
				return nil, nil, fmt.Errorf("truncated at slot %d/%d valLen", i, j)
			}
			valLen := int(data[pos])
			pos++

			if valLen > 0 {
				if pos+valLen > len(data) {
					return nil, nil, fmt.Errorf("truncated at slot %d/%d value", i, j)
				}
				leaf.Value = make([]byte, valLen)
				copy(leaf.Value, data[pos:pos+valLen])
				pos += valLen
			}
			storage = append(storage, leaf)
		}
	}

	return accounts, storage, nil
}
