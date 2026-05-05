// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_reader.go — production witness-backed StateReader.
// Promoted from witness_verify_test.go's witnessReplayReader; identical
// wire format (length-prefixed bytes, code-from-MDBX). The witness
// stream encodes every state read in tx execution order, so the
// replayer just dequeues values in order — addresses and slot keys
// are reconstructed implicitly by re-running the same EVM path.

package ethel

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// WitnessReplayReader implements modules/state.StateReader against a
// witness byte stream + an MDBX RoTx for the Code table. Not
// goroutine-safe — each worker holds its own.
type WitnessReplayReader struct {
	stream []byte
	pos    int
	codeTx kv.Tx
}

func NewWitnessReplayReader(stream []byte, codeTx kv.Tx) *WitnessReplayReader {
	return &WitnessReplayReader{stream: stream, codeTx: codeTx}
}

func (r *WitnessReplayReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil
	}
	acc := &account.StateAccount{}
	if err := acc.DecodeForStorage(r.stream[r.pos : r.pos+length]); err != nil {
		return nil, err
	}
	r.pos += length
	return acc, nil
}

func (r *WitnessReplayReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil
	}
	val := make([]byte, length)
	copy(val, r.stream[r.pos:r.pos+length])
	r.pos += length
	return val, nil
}

func (r *WitnessReplayReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if r.codeTx == nil {
		return nil, nil
	}
	if GlobalBytecodeCache != nil {
		if code, ok := GlobalBytecodeCache.Get(codeHash); ok {
			return code, nil
		}
	}
	code, err := r.codeTx.GetOne("Code", codeHash[:])
	if err != nil {
		return nil, err
	}
	if GlobalBytecodeCache != nil && len(code) > 0 {
		// MDBX returns a slice into mmap memory; copy before
		// caching so the slice survives RoTx rotation.
		cached := make([]byte, len(code))
		copy(cached, code)
		GlobalBytecodeCache.Put(codeHash, cached)
		return cached, nil
	}
	return code, nil
}

func (r *WitnessReplayReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}
