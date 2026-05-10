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
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
)

// WitnessReplayReader implements modules/state.StateReader against a
// witness byte stream + a code source. The code source is either an
// MDBX RoTx (codeHash → bytecode lookup) or a CodesFreezerReader
// (address → bytecode lookup, address-indexed cidx). Not goroutine-
// safe — each worker holds its own.
//
// codes (freezer) is preferred when set: it's address-indexed so it
// works even when account data is incomplete, and it's self-contained
// so witness-replay doesn't need a populated MDBX Code table.
type WitnessReplayReader struct {
	stream []byte
	pos    int
	codeTx kv.Tx               // MDBX Code table (codeHash → code), optional
	codes  *CodesFreezerReader // freezer codes.cidx (addr → code), optional
	// scratch is reused across ReadAccountData calls. All call sites
	// in IntraBlockState do data.Copy(scratch) before the next read,
	// so the returned pointer never aliases stale state. Saves ~5-10K
	// StateAccount allocs per block.
	scratch account.StateAccount
}

func NewWitnessReplayReader(stream []byte, codeTx kv.Tx) *WitnessReplayReader {
	return &WitnessReplayReader{stream: stream, codeTx: codeTx}
}

// SetCodesFreezer attaches an address-indexed code source. The reader
// then looks up bytecode via codes-freezer first, falling back to the
// MDBX codeTx only if codes-freezer is absent.
func (r *WitnessReplayReader) SetCodesFreezer(codes *CodesFreezerReader) {
	r.codes = codes
}

// Reset rebinds the reader to a new witness stream and clears the read
// cursor. codeTx is preserved across calls — workers reuse one RoTx for
// the lifetime of the goroutine.
func (r *WitnessReplayReader) Reset(stream []byte) {
	r.stream = stream
	r.pos = 0
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
	r.scratch = account.StateAccount{}
	if err := r.scratch.DecodeForStorage(r.stream[r.pos : r.pos+length]); err != nil {
		return nil, err
	}
	r.pos += length
	return &r.scratch, nil
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
	// Slice directly into the witness stream — zero alloc on the hot
	// SLOAD path (was the top allocator on long replays). Safety:
	// witness-replay calls state.New(reader) per block, so IBS.snap
	// is nil and stateObject.GetCommittedState's only retaining caller
	// (snap.AddStorage in entire.go:240) is unreachable. The other
	// consumer is uint256.SetBytes which copies on ingest. If snap is
	// ever wired up here, this MUST go back to a copy.
	val := r.stream[r.pos : r.pos+length : r.pos+length]
	r.pos += length
	return val, nil
}

func (r *WitnessReplayReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	// Cache hit (process-wide) — codeHash key. Same key for both code
	// sources because content-addressing is universal.
	if GlobalBytecodeCache != nil {
		if code, ok := GlobalBytecodeCache.Get(codeHash); ok {
			return code, nil
		}
	}
	// Codes-freezer (address-indexed) is tried first when configured.
	// Verify keccak256(stored)==codeHash so a stale or mis-keyed entry
	// surfaces as an error instead of being silently treated as an EOA
	// (which would otherwise drift gas by the missing fallback cost).
	// MDBX has the same content-addressing property by definition (key
	// IS the codeHash) so the fallback below skips the verify.
	if r.codes != nil {
		code, err := r.codes.LookupByAddress(address)
		if err != nil {
			return nil, fmt.Errorf("codes-freezer: %w", err)
		}
		if len(code) > 0 {
			if h := crypto.Keccak256Hash(code); h != codeHash {
				return nil, fmt.Errorf("codes-freezer: stale entry for addr=%x — stored bytecode hashes to %x but witness expects %x; rerun code-import2fz against an up-to-date state DB",
					address[:], h[:], codeHash[:])
			}
			if GlobalBytecodeCache != nil {
				GlobalBytecodeCache.Put(codeHash, code)
			}
			return code, nil
		}
	}
	// Fallback: MDBX Code table (codeHash → bytecode).
	if r.codeTx == nil {
		return nil, nil
	}
	code, err := r.codeTx.GetOne(kv.Code, codeHash[:])
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
