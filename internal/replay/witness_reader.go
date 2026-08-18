// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// WitnessStateReader wraps a state.StateReader and records every
// account, storage and code access performed during block execution.
// The captured maps form the minimal input witness that a stateless
// mobile verifier needs to re-execute the block independently.

package replay

import (
	"bytes"
	"encoding/binary"
	"sort"

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

func (w *WitnessStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	val, err := w.inner.ReadAccountStorage(address, key)
	if err != nil {
		return nil, err
	}
	sk := storageWKey{addr: address, slot: *key}
	if _, recorded := w.storage[sk]; !recorded {
		w.storage[sk] = val
	}
	return val, nil
}

func (w *WitnessStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	code, err := w.inner.ReadAccountCode(address, codeHash)
	if err != nil {
		return nil, err
	}
	if _, recorded := w.code[codeHash]; !recorded {
		w.code[codeHash] = code
	}
	return code, nil
}

func (w *WitnessStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	// Also record code access for witness completeness.
	code, err := w.inner.ReadAccountCode(address, codeHash)
	if err != nil {
		return 0, err
	}
	if _, recorded := w.code[codeHash]; !recorded {
		w.code[codeHash] = code
	}
	return len(code), nil
}

// ForEachStorage enumerates all persisted storage slots of addr via the inner
// reader (implements state.StorageEnumerator) and records each one in the
// witness storage map. Recording is essential: when an account is
// selfdestructed, IntraBlockState enumerates its full pre-block storage to
// delete those slots from the hashed state — a stateless re-execution of the
// same block must therefore have those slots in its witness to reproduce the
// identical root. Slots already recorded by an explicit read are preserved
// (first-write-wins, matching ReadAccountStorage).
func (w *WitnessStateReader) ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error {
	enum, ok := w.inner.(state.StorageEnumerator)
	if !ok {
		return nil
	}
	return enum.ForEachStorage(addr, func(slot types.Hash, value []byte) bool {
		sk := storageWKey{addr: addr, slot: slot}
		if _, recorded := w.storage[sk]; !recorded {
			cp := make([]byte, len(value))
			copy(cp, value)
			w.storage[sk] = cp
		}
		return f(slot, value)
	})
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

	// Accounts. Sorted, like every section below: the three maps are
	// serialized verbatim into modules.BlockWitness, so a Go map walk made
	// the stored witness bytes differ between two runs of the SAME block —
	// the artifact is supposed to be reproducible.
	binary.Write(&buf, binary.BigEndian, uint32(len(w.accounts)))
	addrs := make([]types.Address, 0, len(w.accounts))
	for addr := range w.accounts {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })
	for _, addr := range addrs {
		data := w.accounts[addr]
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
	sks := make([]storageWKey, 0, len(w.storage))
	for sk := range w.storage {
		sks = append(sks, sk)
	}
	sort.Slice(sks, func(i, j int) bool {
		if c := bytes.Compare(sks[i].addr[:], sks[j].addr[:]); c != 0 {
			return c < 0
		}
		if sks[i].inc != sks[j].inc {
			return sks[i].inc < sks[j].inc
		}
		return bytes.Compare(sks[i].slot[:], sks[j].slot[:]) < 0
	})
	for _, sk := range sks {
		val := w.storage[sk]
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
	hashes := make([]types.Hash, 0, len(w.code))
	for h := range w.code {
		hashes = append(hashes, h)
	}
	sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
	for _, hash := range hashes {
		code := w.code[hash]
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
