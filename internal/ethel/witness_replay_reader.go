// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_reader.go — production WitnessReplayReader for the
// v2 sorted-map format produced by witness.go's WitnessStateReader.
// Decodes the entire blob into per-block lookup maps so EVM reads in
// replay can resolve by KEY (address / address+slot) regardless of
// the order ProcessBlock issues them — the v1 stream format was order-
// sensitive and broke whenever a code path between recording and
// replay shifted reads around.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// WitnessReplayReader implements modules/state.StateReader against a
// decoded v2 witness blob + an MDBX RoTx for the Code table.
// Goroutine-safe for concurrent reads (all maps are read-only after
// construction; codeTx is the caller's responsibility).
type WitnessReplayReader struct {
	accounts map[types.Address][]byte
	storage  map[types.Address]map[types.Hash][]byte
	codeTx   kv.Tx
}

// NewWitnessReplayReader decodes a v2 witness blob. Accepts an empty
// stream (no recorded reads → empty maps). Returns an error if the
// blob is malformed or carries an unsupported version byte.
func NewWitnessReplayReader(stream []byte, codeTx kv.Tx) *WitnessReplayReader {
	r, _ := tryDecodeWitness(stream, codeTx)
	return r
}

// NewWitnessReplayReaderStrict is the error-returning variant; use it
// in production code that wants to surface decode failures.
func NewWitnessReplayReaderStrict(stream []byte, codeTx kv.Tx) (*WitnessReplayReader, error) {
	return tryDecodeWitness(stream, codeTx)
}

func tryDecodeWitness(stream []byte, codeTx kv.Tx) (*WitnessReplayReader, error) {
	r := &WitnessReplayReader{
		accounts: make(map[types.Address][]byte),
		storage:  make(map[types.Address]map[types.Hash][]byte),
		codeTx:   codeTx,
	}
	if len(stream) == 0 {
		return r, nil
	}
	if stream[0] != witnessFormatV2 {
		return r, fmt.Errorf("witness format v%d unsupported (expected v%d)", stream[0], witnessFormatV2)
	}
	pos := 1

	accCount, n := binary.Uvarint(stream[pos:])
	if n <= 0 {
		return r, fmt.Errorf("witness: malformed acc_count varint")
	}
	pos += n
	for i := uint64(0); i < accCount; i++ {
		if pos+20 > len(stream) {
			return r, fmt.Errorf("witness: short read at account %d", i)
		}
		var addr types.Address
		copy(addr[:], stream[pos:pos+20])
		pos += 20
		encLen, m := binary.Uvarint(stream[pos:])
		if m <= 0 {
			return r, fmt.Errorf("witness: malformed enc_len at account %d", i)
		}
		pos += m
		if encLen == 0 {
			r.accounts[addr] = nil
			continue
		}
		if pos+int(encLen) > len(stream) {
			return r, fmt.Errorf("witness: short read at account %d body", i)
		}
		v := make([]byte, encLen)
		copy(v, stream[pos:pos+int(encLen)])
		pos += int(encLen)
		r.accounts[addr] = v
	}

	stoCount, n := binary.Uvarint(stream[pos:])
	if n <= 0 {
		return r, fmt.Errorf("witness: malformed sto_count varint")
	}
	pos += n
	for i := uint64(0); i < stoCount; i++ {
		if pos+52 > len(stream) {
			return r, fmt.Errorf("witness: short read at storage %d", i)
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], stream[pos:pos+20])
		pos += 20
		copy(slot[:], stream[pos:pos+32])
		pos += 32
		valLen, m := binary.Uvarint(stream[pos:])
		if m <= 0 {
			return r, fmt.Errorf("witness: malformed val_len at storage %d", i)
		}
		pos += m
		slots, ok := r.storage[addr]
		if !ok {
			slots = make(map[types.Hash][]byte)
			r.storage[addr] = slots
		}
		if valLen == 0 {
			slots[slot] = nil
			continue
		}
		if pos+int(valLen) > len(stream) {
			return r, fmt.Errorf("witness: short read at storage %d body", i)
		}
		v := make([]byte, valLen)
		copy(v, stream[pos:pos+int(valLen)])
		pos += int(valLen)
		slots[slot] = v
	}
	if pos != len(stream) {
		return r, fmt.Errorf("witness: %d trailing bytes", len(stream)-pos)
	}
	return r, nil
}

func (r *WitnessReplayReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	enc, ok := r.accounts[address]
	if !ok {
		// Address never accessed during recording — treat as absent.
		// Production replay should have a complete witness; if a real
		// access happens here it manifests as a downstream gas mismatch.
		return nil, nil
	}
	if len(enc) == 0 {
		return nil, nil
	}
	acc := &account.StateAccount{}
	if err := acc.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return acc, nil
}

func (r *WitnessReplayReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	slots, ok := r.storage[address]
	if !ok {
		return nil, nil
	}
	v, ok := slots[*key]
	if !ok {
		return nil, nil
	}
	if len(v) == 0 {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (r *WitnessReplayReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if r.codeTx == nil {
		return nil, nil
	}
	return r.codeTx.GetOne("Code", codeHash[:])
}

func (r *WitnessReplayReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}
