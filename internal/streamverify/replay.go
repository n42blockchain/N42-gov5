// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package streamverify

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// ErrNotRecorded is returned by a RecordedStateReader asked for state the
// source recorder never read. The rebuilt log cannot be produced from the
// recorded values alone; the caller must drop the packet, never guess.
var ErrNotRecorded = errors.New("streamverify: read was not recorded during the build")

// RecordedStateReader answers reads from the values a ReadLogRecorder captured
// from the parent state during the build, keyed by account and slot. It never
// touches the recorder's inner reader, so it stays usable after the build's
// database transaction is gone.
//
// Accounts are served exactly as the verifier's StreamReplayStateReader
// rebuilds them from a log entry (nonce, balance, code hash), so a rebuild
// over this reader executes what a device will execute.
type RecordedStateReader struct {
	accounts map[types.Address]evmsdk.ReadLogEntry
	storage  map[readKey]evmsdk.ReadLogStorage
	codes    map[types.Hash][]byte
	known    map[types.Hash][]byte
	err      error
}

// ReplayRecorder returns a fresh recorder, with the same known-code set, over
// a RecordedStateReader built from what r captured. Re-executing the block's
// transactions through it yields the read log a verifier consumes, without
// the reads of anything the build threw away. The returned reader's Err must
// be checked after the re-execution.
//
// r must not be recording any more (the build is done).
func (r *ReadLogRecorder) ReplayRecorder() (*ReadLogRecorder, *RecordedStateReader) {
	rr := &RecordedStateReader{
		accounts: make(map[types.Address]evmsdk.ReadLogEntry),
		storage:  make(map[readKey]evmsdk.ReadLogStorage),
		codes:    r.captured,
		known:    r.knownSeen,
	}
	for i, k := range r.keys {
		// The parent state does not change during a build, so a key read
		// twice was read with the same value; keep the first.
		if k.storage {
			if _, ok := rr.storage[k]; !ok {
				if e, ok := r.log[i].(evmsdk.ReadLogStorage); ok {
					rr.storage[k] = e
				}
			}
			continue
		}
		if _, ok := rr.accounts[k.addr]; !ok {
			rr.accounts[k.addr] = r.log[i]
		}
	}
	return NewReadLogRecorder(rr, r.knownCodes), rr
}

// Err returns the first read the recorded values could not answer.
func (r *RecordedStateReader) Err() error { return r.err }

func (r *RecordedStateReader) fail(err error) error {
	if r.err == nil {
		r.err = err
	}
	return err
}

// ReadAccountData serves a recorded account read.
func (r *RecordedStateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	e, ok := r.accounts[addr]
	if !ok {
		return nil, r.fail(fmt.Errorf("%w: account %v", ErrNotRecorded, addr))
	}
	switch v := e.(type) {
	case evmsdk.ReadLogAccountNotFound:
		return nil, nil
	case evmsdk.ReadLogAccount:
		// Same construction as evmsdk.StreamReplayStateReader.ReadAccountData.
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Nonce = v.Nonce
		if v.Balance != nil {
			acc.Balance.Set(v.Balance)
		}
		acc.CodeHash = v.CodeHash
		return &acc, nil
	default:
		return nil, r.fail(fmt.Errorf("%w: account %v logged as %T", ErrNotRecorded, addr, e))
	}
}

// ReadAccountStorage serves a recorded storage read.
func (r *RecordedStateReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	e, ok := r.storage[readKey{addr: addr, slot: *key, storage: true}]
	if !ok {
		return nil, r.fail(fmt.Errorf("%w: storage %v/%v", ErrNotRecorded, addr, *key))
	}
	if e.Value == nil || e.Value.IsZero() {
		return nil, nil
	}
	return e.Value.Bytes(), nil
}

// ReadAccountCode serves a bytecode the build read.
func (r *RecordedStateReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if codeHash == evmsdk.EmptyCodeHash {
		return nil, nil
	}
	if code, ok := r.codes[codeHash]; ok {
		return code, nil
	}
	if code, ok := r.known[codeHash]; ok {
		return code, nil
	}
	return nil, r.fail(fmt.Errorf("%w: code %v of %v", ErrNotRecorded, codeHash, addr))
}

// ReadAccountCodeSize returns the length of a bytecode the build read.
func (r *RecordedStateReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// Compile-time interface check.
var _ state.StateReader = (*RecordedStateReader)(nil)
