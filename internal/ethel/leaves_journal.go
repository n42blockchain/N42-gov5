// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/changeset"
)

// EncodeLeavesJournal builds a leaves journal from changesets.
// Records the hashed key + NEW value (post-execution) for every changed leaf.
//
// Account section:
//
//	[numAccounts:4LE]
//	per account: [hashedAddr:32][valLen:1][newValue:N]
//
// Storage section (address-grouped to avoid repeating hashedAddr):
//
//	[numAddrs:4LE]
//	per address:
//	  [hashedAddr:32][numSlots:2LE]
//	  per slot: [hashedSlot:32][valLen:1][newValue:0-32]
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
			buf = append(buf, crypto.Keccak256(c.Key[:20])...)

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
		hashedSlot [32]byte
		value      []byte
	}
	type addrLeaves struct {
		hashedAddr [32]byte
		plainAddr  types.Address
		slots      []slotLeaf
	}

	var groups []addrLeaves
	var lastAddr [20]byte
	var lastHashedAddr [32]byte
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
				copy(lastHashedAddr[:], crypto.Keccak256(thisAddr[:]))
				groups = append(groups, addrLeaves{hashedAddr: lastHashedAddr})
				copy(groups[len(groups)-1].plainAddr[:], thisAddr[:])
				cur = &groups[len(groups)-1]
			}

			var hs [32]byte
			copy(hs[:], crypto.Keccak256(c.Key[22:54]))

			var addr types.Address
			copy(addr[:], c.Key[:20])
			var slotKey types.Hash
			copy(slotKey[:], c.Key[22:54])
			val := getStorage(addr, slotKey)

			cur.slots = append(cur.slots, slotLeaf{hashedSlot: hs, value: val})
		}
	}

	addrCountPos := len(buf)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(groups)))
	for _, g := range groups {
		buf = append(buf, g.hashedAddr[:]...)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(g.slots)))
		for _, s := range g.slots {
			buf = append(buf, s.hashedSlot[:]...)
			if s.value == nil {
				buf = append(buf, 0)
			} else {
				buf = append(buf, byte(len(s.value)))
				buf = append(buf, s.value...)
			}
		}
	}
	_ = addrCountPos // already written inline

	return buf
}
