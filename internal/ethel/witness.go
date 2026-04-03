// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// WitnessStateReader wraps a StateReader and records every state access.
// After block execution, the recorded accesses form the BlockWitness —
// the minimal state subset needed to replay the block without full DB.
type WitnessStateReader struct {
	inner    state.StateReader
	accounts map[types.Address]*account.StateAccount // accessed accounts (nil = confirmed absent)
	storage  map[types.Address]map[types.Hash][]byte // accessed storage slots
	// Code is intentionally NOT recorded (user requirement: witness without code).
}

func NewWitnessStateReader(inner state.StateReader) *WitnessStateReader {
	return &WitnessStateReader{
		inner:    inner,
		accounts: make(map[types.Address]*account.StateAccount),
		storage:  make(map[types.Address]map[types.Hash][]byte),
	}
}

func (w *WitnessStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acc, err := w.inner.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if _, seen := w.accounts[address]; !seen {
		w.accounts[address] = acc // nil means account doesn't exist
	}
	return acc, nil
}

func (w *WitnessStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	val, err := w.inner.ReadAccountStorage(address, incarnation, key)
	if err != nil {
		return nil, err
	}
	if w.storage[address] == nil {
		w.storage[address] = make(map[types.Hash][]byte)
	}
	if _, seen := w.storage[address][*key]; !seen {
		w.storage[address][*key] = val // nil/empty means slot doesn't exist
	}
	return val, nil
}

func (w *WitnessStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	// Delegate but do NOT record (witness excludes code).
	return w.inner.ReadAccountCode(address, incarnation, codeHash)
}

func (w *WitnessStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	return w.inner.ReadAccountCodeSize(address, incarnation, codeHash)
}

func (w *WitnessStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return w.inner.ReadAccountIncarnation(address)
}

// Encode serializes the witness as flat bytes:
//
//	[numAccounts:4LE]
//	  for each: [addr:20][hasData:1][accountV2...]
//	[numStorageAddrs:4LE]
//	  for each: [addr:20][numSlots:4LE]
//	    for each slot: [key:32][valLen:2LE][val...]
func (w *WitnessStateReader) Encode() []byte {
	// Pre-calculate size estimate.
	buf := make([]byte, 0, 4096)

	// Accounts.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(w.accounts)))
	for addr, acc := range w.accounts {
		buf = append(buf, addr[:]...)
		if acc == nil {
			buf = append(buf, 0) // absent
		} else {
			buf = append(buf, 1) // present
			encoded := acc.MarshalV2()
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(encoded)))
			buf = append(buf, encoded...)
		}
	}

	// Storage.
	storageAddrs := 0
	for _, slots := range w.storage {
		if len(slots) > 0 {
			storageAddrs++
		}
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(storageAddrs))
	for addr, slots := range w.storage {
		if len(slots) == 0 {
			continue
		}
		buf = append(buf, addr[:]...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(slots)))
		for key, val := range slots {
			buf = append(buf, key[:]...)
			if val == nil {
				buf = binary.LittleEndian.AppendUint16(buf, 0)
			} else {
				buf = binary.LittleEndian.AppendUint16(buf, uint16(len(val)))
				buf = append(buf, val...)
			}
		}
	}

	return buf
}

// Reset clears recorded state for reuse.
func (w *WitnessStateReader) Reset() {
	w.accounts = make(map[types.Address]*account.StateAccount)
	w.storage = make(map[types.Address]map[types.Hash][]byte)
}
