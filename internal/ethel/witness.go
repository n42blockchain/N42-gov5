// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness.go — block-witness recorder wrapping a StateReader.
//
// V2 sorted-map format. Each block's witness is a deduplicated set of
// (address → encoded-account) and (address+slot → raw-value) entries
// emitted in canonical sort order at Encode() time. Replay looks up by
// EVM-supplied key, so the access order during re-execution can differ
// from the order during recording — fixing the v1 stream format's
// fragility to ProcessBlock changes between recording and replay
// (state-read order drift produced spurious gas mismatches).
//
// Code is still excluded from the witness (content-addressed, recovered
// from the MDBX Code table during replay).

package ethel

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// witnessFormatV2 is the magic byte at the head of the encoded blob.
const witnessFormatV2 byte = 2

// WitnessStateReader wraps a StateReader and records every state read.
// Encode() emits the deduplicated entries sorted by key; replay looks
// up by key and is therefore independent of access order.
type WitnessStateReader struct {
	inner    state.StateReader
	accounts map[types.Address][]byte               // encoded V2 bytes; nil-value = absent
	storage  map[types.Address]map[types.Hash][]byte // raw bytes; nil = empty slot
}

func NewWitnessStateReader(inner state.StateReader) *WitnessStateReader {
	return &WitnessStateReader{
		inner:    inner,
		accounts: make(map[types.Address][]byte, 64),
		storage:  make(map[types.Address]map[types.Hash][]byte, 32),
	}
}

func (w *WitnessStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acc, err := w.inner.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if _, seen := w.accounts[address]; !seen {
		if acc != nil {
			w.accounts[address] = acc.MarshalV2()
		} else {
			w.accounts[address] = nil
		}
	}
	return acc, nil
}

func (w *WitnessStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	val, err := w.inner.ReadAccountStorage(address, key)
	if err != nil {
		return nil, err
	}
	slots, ok := w.storage[address]
	if !ok {
		slots = make(map[types.Hash][]byte, 8)
		w.storage[address] = slots
	}
	if _, seen := slots[*key]; !seen {
		if len(val) > 0 {
			cp := make([]byte, len(val))
			copy(cp, val)
			slots[*key] = cp
		} else {
			slots[*key] = nil
		}
	}
	return val, nil
}

func (w *WitnessStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	// Delegate; code is content-addressed and recovered from the
	// MDBX Code table at replay time.
	return w.inner.ReadAccountCode(address, codeHash)
}

func (w *WitnessStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	return w.inner.ReadAccountCodeSize(address, codeHash)
}

// Encode emits the sorted v2 wire format. Empty (no recorded reads)
// returns an empty slice so freezer entries stay tiny.
//
// Layout:
//
//	[version: 1]
//	[acc_count: varint]
//	  per addr (sorted asc):
//	    [addr: 20]
//	    [enc_len: varint][enc: enc_len]    (enc_len=0 means absent)
//	[sto_count: varint]
//	  per (addr, slot) (sorted asc by addr then slot):
//	    [addr: 20][slot: 32]
//	    [val_len: varint][val: val_len]    (val_len=0 means empty slot)
func (w *WitnessStateReader) Encode() []byte {
	if len(w.accounts) == 0 && len(w.storage) == 0 {
		return nil
	}
	addrs := make([]types.Address, 0, len(w.accounts))
	for a := range w.accounts {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })

	type stoKey struct {
		addr types.Address
		slot types.Hash
	}
	var stoKeys []stoKey
	stoCount := 0
	for _, slots := range w.storage {
		stoCount += len(slots)
	}
	stoKeys = make([]stoKey, 0, stoCount)
	for addr, slots := range w.storage {
		for slot := range slots {
			stoKeys = append(stoKeys, stoKey{addr, slot})
		}
	}
	sort.Slice(stoKeys, func(i, j int) bool {
		if c := bytes.Compare(stoKeys[i].addr[:], stoKeys[j].addr[:]); c != 0 {
			return c < 0
		}
		return bytes.Compare(stoKeys[i].slot[:], stoKeys[j].slot[:]) < 0
	})

	// Pre-size buffer (rough): version + 2 varints + per-account 20+5+enc + per-storage 52+5+val
	buf := make([]byte, 0, 1+10+10+len(addrs)*60+len(stoKeys)*70)
	buf = append(buf, witnessFormatV2)
	buf = appendUvarint(buf, uint64(len(addrs)))
	for _, addr := range addrs {
		buf = append(buf, addr[:]...)
		v := w.accounts[addr]
		buf = appendUvarint(buf, uint64(len(v)))
		buf = append(buf, v...)
	}
	buf = appendUvarint(buf, uint64(len(stoKeys)))
	for _, k := range stoKeys {
		buf = append(buf, k.addr[:]...)
		buf = append(buf, k.slot[:]...)
		v := w.storage[k.addr][k.slot]
		buf = appendUvarint(buf, uint64(len(v)))
		buf = append(buf, v...)
	}
	return buf
}

// Reset clears recorded entries for reuse across blocks.
func (w *WitnessStateReader) Reset() {
	for k := range w.accounts {
		delete(w.accounts, k)
	}
	for k := range w.storage {
		delete(w.storage, k)
	}
}

func appendUvarint(buf []byte, v uint64) []byte {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}
