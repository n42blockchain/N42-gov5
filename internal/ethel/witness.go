// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// WitnessStateReader wraps a StateReader and records every state access
// value in execution order. The resulting stream is the BlockWitness —
// a sequence of length-prefixed values that can replay the block's state
// reads without addresses or keys (the replayer knows the access pattern).
//
// Code is intentionally NOT recorded.
type WitnessStateReader struct {
	inner  state.StateReader
	stream []byte // sequential: [len:1][data:len] per access
}

func NewWitnessStateReader(inner state.StateReader) *WitnessStateReader {
	return &WitnessStateReader{
		inner:  inner,
		stream: make([]byte, 0, 4096),
	}
}

func (w *WitnessStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acc, err := w.inner.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		w.stream = append(w.stream, 0) // absent: len=0
	} else {
		encoded := acc.MarshalV2()
		w.stream = append(w.stream, byte(len(encoded)))
		w.stream = append(w.stream, encoded...)
	}
	return acc, nil
}

func (w *WitnessStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	val, err := w.inner.ReadAccountStorage(address, incarnation, key)
	if err != nil {
		return nil, err
	}
	if len(val) == 0 {
		w.stream = append(w.stream, 0) // absent/empty: len=0
	} else {
		w.stream = append(w.stream, byte(len(val)))
		w.stream = append(w.stream, val...)
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

// Encode returns the witness stream bytes.
// Format: sequential [len:1byte][value:len bytes] per state access,
// in exact execution order. No addresses, no keys, no counts.
// The replayer knows which account/slot each entry corresponds to
// by re-executing the same transactions.
func (w *WitnessStateReader) Encode() []byte {
	return w.stream
}

// Reset clears the stream for reuse.
func (w *WitnessStateReader) Reset() {
	w.stream = w.stream[:0]
}
