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

// EncodeLeavesJournal builds a leaves journal entry from account and storage
// changesets. Each entry records the hashed key and NEW value (post-execution)
// for every changed leaf in the block.
//
// Format:
//
//	[numAccountLeaves:4LE]
//	  for each: [hashedAddr:32][valLen:2LE][newValue...]  (valLen=0 means deleted)
//	[numStorageLeaves:4LE]
//	  for each: [hashedAddr:32][hashedSlot:32][valLen:2LE][newValue...]
func EncodeLeavesJournal(
	accCS *changeset.ChangeSet,
	stoCS *changeset.ChangeSet,
	getAccount func(addr types.Address) *account.StateAccount,
	getStorage func(addr types.Address, key types.Hash) []byte,
) []byte {
	buf := make([]byte, 0, 4096)

	// Account leaves: changeset keys are plain addresses.
	numAcc := 0
	if accCS != nil {
		numAcc = accCS.Len()
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(numAcc))
	if accCS != nil {
		for _, c := range accCS.Changes {
			if len(c.Key) < 20 {
				continue
			}
			var addr types.Address
			copy(addr[:], c.Key[:20])
			hashedAddr := crypto.Keccak256(addr[:])
			buf = append(buf, hashedAddr...)

			// Get the NEW value (post-execution) from current state.
			acc := getAccount(addr)
			if acc == nil {
				buf = binary.LittleEndian.AppendUint16(buf, 0) // deleted
			} else {
				encoded := acc.MarshalV2()
				buf = binary.LittleEndian.AppendUint16(buf, uint16(len(encoded)))
				buf = append(buf, encoded...)
			}
		}
	}

	// Storage leaves: changeset keys are compositeKey (addr+inc+slot).
	numSto := 0
	if stoCS != nil {
		numSto = stoCS.Len()
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(numSto))
	if stoCS != nil {
		for _, c := range stoCS.Changes {
			if len(c.Key) < 54 { // addr(20)+inc(2)+slot(32)
				continue
			}
			hashedAddr := crypto.Keccak256(c.Key[:20])
			hashedSlot := crypto.Keccak256(c.Key[22:54])
			buf = append(buf, hashedAddr...)
			buf = append(buf, hashedSlot...)

			// Get the NEW value from current state.
			var addr types.Address
			copy(addr[:], c.Key[:20])
			var slotKey types.Hash
			copy(slotKey[:], c.Key[22:54])
			val := getStorage(addr, slotKey)
			if val == nil {
				buf = binary.LittleEndian.AppendUint16(buf, 0) // deleted
			} else {
				buf = binary.LittleEndian.AppendUint16(buf, uint16(len(val)))
				buf = append(buf, val...)
			}
		}
	}

	return buf
}
