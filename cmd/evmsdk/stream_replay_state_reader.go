// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// StreamReplayStateReader is the cursor-based state.StateReader that powers
// mobile parallel verification. The IDC node executes a block with an
// instrumented reader that records every state read into an ordered log.
// The mobile side replays the same block with this reader, consuming the
// log in call order. Because both sides run the same Go EVM, the read
// order is deterministic and the cursor stays in sync without needing
// explicit address/slot keys.
//
// Mirrors the design of D:\N42\n42-26\crates\n42-mobile\src\verifier.rs
// (StreamReplayDB), but adapted to the N42-gov5 state.StateReader
// interface (5 methods on plain addresses, vs revm's keyed Database).

package evmsdk

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// ErrReplayCursorExhausted is returned when the EVM asks for more state
// than the read log holds. Indicates either a corrupted packet or a
// non-determinism bug between IDC and mobile EVM versions.
var ErrReplayCursorExhausted = errors.New("stream_replay: read log exhausted; producer/verifier EVM disagreed on call order")

// ErrReplayUnexpectedEntry is returned when the next log entry has a
// different variant than the EVM call expects. Same root cause as the
// exhausted error: an order/version mismatch.
var ErrReplayUnexpectedEntry = errors.New("stream_replay: unexpected entry type; producer/verifier EVM disagreed on call order")

// StreamReplayStateReader implements state.StateReader by walking a
// read log in call order. Account/storage reads are answered from the
// log; code reads are answered from the shared CodeCache (populated
// from packet.Bytecodes before replay starts).
//
// On any error the reader records lastErr so the orchestrator can
// surface a single root cause after the EVM bails out.
type StreamReplayStateReader struct {
	entries []ReadLogEntry
	cursor  int
	codes   *CodeCache
	lastErr error
}

// NewStreamReplayStateReader builds a reader over the given log + cache.
// The CodeCache should already contain every uncached bytecode the
// packet shipped, plus whatever the phone has cached locally.
func NewStreamReplayStateReader(log []ReadLogEntry, codes *CodeCache) *StreamReplayStateReader {
	return &StreamReplayStateReader{entries: log, codes: codes}
}

// Err returns the first replay error encountered, or nil if all reads
// matched the log so far.
func (r *StreamReplayStateReader) Err() error { return r.lastErr }

// Cursor returns the next entry index. Useful for tests.
func (r *StreamReplayStateReader) Cursor() int { return r.cursor }

// Remaining is the number of un-consumed log entries.
func (r *StreamReplayStateReader) Remaining() int { return len(r.entries) - r.cursor }

func (r *StreamReplayStateReader) recordErr(err error) {
	if r.lastErr == nil {
		r.lastErr = err
	}
}

func (r *StreamReplayStateReader) next() (ReadLogEntry, bool) {
	if r.cursor >= len(r.entries) {
		r.recordErr(fmt.Errorf("%w (at cursor %d, total %d)", ErrReplayCursorExhausted, r.cursor, len(r.entries)))
		return nil, false
	}
	e := r.entries[r.cursor]
	r.cursor++
	return e, true
}

// ReadAccountData consumes one log entry, expecting Account or AccountNotFound.
func (r *StreamReplayStateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	entry, ok := r.next()
	if !ok {
		return nil, r.lastErr
	}
	switch v := entry.(type) {
	case ReadLogAccountNotFound:
		// Account doesn't exist — convention is (nil, nil).
		return nil, nil
	case ReadLogAccount:
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Nonce = v.Nonce
		if v.Balance != nil {
			acc.Balance.Set(v.Balance)
		}
		acc.CodeHash = v.CodeHash
		return &acc, nil
	default:
		err := fmt.Errorf("%w: ReadAccountData(%x) at cursor %d got %T", ErrReplayUnexpectedEntry, addr, r.cursor-1, entry)
		r.recordErr(err)
		return nil, err
	}
}

// ReadAccountStorage consumes one Storage log entry.
//
// The convention in N42 is that an empty storage slot returns (nil, nil).
// We translate the log's Storage(0) to nil to match the existing
// PlainStateReader behavior.
func (r *StreamReplayStateReader) ReadAccountStorage(addr types.Address, _ uint16, key *types.Hash) ([]byte, error) {
	entry, ok := r.next()
	if !ok {
		return nil, r.lastErr
	}
	v, ok := entry.(ReadLogStorage)
	if !ok {
		err := fmt.Errorf("%w: ReadAccountStorage(%x,%x) at cursor %d got %T", ErrReplayUnexpectedEntry, addr, key, r.cursor-1, entry)
		r.recordErr(err)
		return nil, err
	}
	if v.Value == nil || v.Value.IsZero() {
		return nil, nil
	}
	// uint256.Int.Bytes() already returns a fresh trimmed big-endian slice
	// (PlainStateReader convention), so we hand it out directly.
	return v.Value.Bytes(), nil
}

// ReadAccountCode pulls bytecode from the shared cache, NOT the log.
//
// The producer side captures bytecodes once per packet and ships them in
// packet.Bytecodes. The orchestrator preloads those into the cache before
// calling the EVM. If the EVM asks for a code hash the cache doesn't
// have, the packet was malformed.
func (r *StreamReplayStateReader) ReadAccountCode(addr types.Address, _ uint16, codeHash types.Hash) ([]byte, error) {
	if codeHash == emptyCodeHash {
		return nil, nil
	}
	if r.codes == nil {
		err := fmt.Errorf("stream_replay: code cache nil for hash %x", codeHash)
		r.recordErr(err)
		return nil, err
	}
	code, ok := r.codes.Get(codeHash)
	if !ok {
		err := fmt.Errorf("stream_replay: missing bytecode for code hash %x", codeHash)
		r.recordErr(err)
		return nil, err
	}
	return code, nil
}

// ReadAccountCodeSize delegates to ReadAccountCode for simplicity. The
// extra map lookup is negligible compared to EVM work.
func (r *StreamReplayStateReader) ReadAccountCodeSize(addr types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, incarnation, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// ReadAccountIncarnation always returns 0. Incarnation tracking has been
// removed from the EthEL replay path (see modules/state/buffered_plain_state.go).
func (r *StreamReplayStateReader) ReadAccountIncarnation(types.Address) (uint16, error) {
	return 0, nil
}

// Compile-time check.
var _ state.StateReader = (*StreamReplayStateReader)(nil)
