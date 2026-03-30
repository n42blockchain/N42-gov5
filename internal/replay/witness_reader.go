// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"bytes"
	"encoding/binary"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// WitnessStateReader wraps a StateReader and records every state access.
// After block execution, the collected data is the minimal "input witness"
// a stateless client (mobile SDK) needs to verify that block independently.
type WitnessStateReader struct {
	inner    state.StateReader
	accounts map[types.Address][]byte // address → encoded account (nil = not found)
	storage  map[storageWKey][]byte   // (addr,inc,slot) → value
	code     map[types.Hash][]byte    // codeHash → code bytes
}

type storageWKey struct {
	addr types.Address
	inc  uint16
	slot types.Hash
}

func NewWitnessStateReader(inner state.StateReader) *WitnessStateReader {
	return &WitnessStateReader{
		inner:    inner,
		accounts: make(map[types.Address][]byte),
		storage:  make(map[storageWKey][]byte),
		code:     make(map[types.Hash][]byte),
	}
}

func (w *WitnessStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acct, err := w.inner.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if _, recorded := w.accounts[address]; !recorded {
		if acct == nil {
			w.accounts[address] = nil
		} else {
			w.accounts[address] = acct.MarshalV2()
		}
	}
	return acct, nil
}

func (w *WitnessStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	val, err := w.inner.ReadAccountStorage(address, incarnation, key)
	if err != nil {
		return nil, err
	}
	sk := storageWKey{addr: address, inc: incarnation, slot: *key}
	if _, recorded := w.storage[sk]; !recorded {
		w.storage[sk] = val
	}
	return val, nil
}

func (w *WitnessStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	code, err := w.inner.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return nil, err
	}
	if _, recorded := w.code[codeHash]; !recorded {
		w.code[codeHash] = code
	}
	return code, nil
}

func (w *WitnessStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	// Also record code access for witness completeness.
	code, err := w.inner.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return 0, err
	}
	if _, recorded := w.code[codeHash]; !recorded {
		w.code[codeHash] = code
	}
	return len(code), nil
}

func (w *WitnessStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return w.inner.ReadAccountIncarnation(address)
}

// Serialize encodes the collected witness as a compact byte slice.
// Format:
//
//	[accountCount:4B]
//	  [address:20B][dataLen:2B][data:N] ...
//	[storageCount:4B]
//	  [address:20B][incarnation:2B][slot:32B][valLen:2B][val:N] ...
//	[codeCount:4B]
//	  [codeHash:32B][codeLen:4B][code:N] ...
//
// Empty witness (no state access) returns nil.
func (w *WitnessStateReader) Serialize() []byte {
	if len(w.accounts) == 0 && len(w.storage) == 0 && len(w.code) == 0 {
		return nil
	}

	var buf bytes.Buffer

	// Accounts
	binary.Write(&buf, binary.BigEndian, uint32(len(w.accounts)))
	for addr, data := range w.accounts {
		buf.Write(addr[:])
		if data == nil {
			binary.Write(&buf, binary.BigEndian, uint16(0))
		} else {
			binary.Write(&buf, binary.BigEndian, uint16(len(data)))
			buf.Write(data)
		}
	}

	// Storage
	binary.Write(&buf, binary.BigEndian, uint32(len(w.storage)))
	for sk, val := range w.storage {
		buf.Write(sk.addr[:])
		binary.Write(&buf, binary.BigEndian, sk.inc)
		buf.Write(sk.slot[:])
		if val == nil {
			binary.Write(&buf, binary.BigEndian, uint16(0))
		} else {
			binary.Write(&buf, binary.BigEndian, uint16(len(val)))
			buf.Write(val)
		}
	}

	// Code
	binary.Write(&buf, binary.BigEndian, uint32(len(w.code)))
	for hash, code := range w.code {
		buf.Write(hash[:])
		if code == nil {
			binary.Write(&buf, binary.BigEndian, uint32(0))
		} else {
			binary.Write(&buf, binary.BigEndian, uint32(len(code)))
			buf.Write(code)
		}
	}

	return buf.Bytes()
}

// Reset clears the recorded state for the next block.
func (w *WitnessStateReader) Reset() {
	clear(w.accounts)
	clear(w.storage)
	clear(w.code)
}

// Len returns total recorded entries.
func (w *WitnessStateReader) Len() int {
	return len(w.accounts) + len(w.storage) + len(w.code)
}
