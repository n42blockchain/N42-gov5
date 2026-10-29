// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package streamverify is the IDC-side toolkit for the mobile parallel
// verification track. It captures the per-block read log and uncached
// bytecodes during EVM execution and packages them into a StreamPacket
// that mobile verifiers (cmd/evmsdk) re-execute and BLS-sign.
//
// Imports cmd/evmsdk for the canonical wire types (StreamPacket,
// ReadLogEntry, EncodeReadLog). The dep direction is inverted from the
// usual internal/* convention but acyclic. Compare with
// internal/ethel/witness.go's WitnessStateReader, which records reads
// for a different downstream consumer (offline replay) and uses a
// different wire format (length-prefixed raw bytes, no typed entries).

package streamverify

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// ReadLogRecorder wraps a state.StateReader and captures every read into
// an ordered log + uncached bytecode map. Pass it to state.New() in
// place of the real reader during block execution; afterwards call
// Log() and UncachedBytecodes() to get the captured data for packet
// assembly.
//
// Thread Safety: NOT goroutine-safe. The Go EVM executes a block
// sequentially on a single thread; one recorder per block.
type ReadLogRecorder struct {
	inner      state.StateReader
	log        []evmsdk.ReadLogEntry
	captured   map[types.Hash][]byte
	knownCodes map[types.Hash]struct{}

	// keys[i] names what log[i] was read for. The wire log carries no keys;
	// these exist so the log can be rebuilt from the recorded values alone
	// (ReplayRecorder) once the execution that produced it stopped being a
	// replay of the block's transactions (MarkDiverged).
	keys []readKey
	// knownSeen holds the known-set bytecodes this recorder served, which
	// captured omits by design; a rebuild needs every code that was read.
	knownSeen map[types.Hash][]byte
	diverged  bool
}

// readKey identifies one logged read: an account, or one of its slots.
type readKey struct {
	addr    types.Address
	slot    types.Hash
	storage bool
}

// NewReadLogRecorder wraps inner with read capture.
//
// knownCodes is the set of code hashes that consumers (mobile clients)
// already have cached. Bytecodes for these hashes are NOT included in
// the captured map, keeping packet size small as the cluster of
// connected verifiers warms up.
//
// Pass nil for knownCodes to capture every bytecode read (full cold start).
func NewReadLogRecorder(inner state.StateReader, knownCodes map[types.Hash]struct{}) *ReadLogRecorder {
	if knownCodes == nil {
		knownCodes = map[types.Hash]struct{}{}
	}
	return &ReadLogRecorder{
		inner:      inner,
		captured:   make(map[types.Hash][]byte),
		knownCodes: knownCodes,
	}
}

// Log returns the captured read log in the order the EVM made the calls.
// The slice is owned by the recorder; do not mutate.
func (r *ReadLogRecorder) Log() []evmsdk.ReadLogEntry { return r.log }

// UncachedBytecodes returns the captured bytecodes that were not in the
// known set. The map is owned by the recorder; do not mutate.
func (r *ReadLogRecorder) UncachedBytecodes() map[types.Hash][]byte { return r.captured }

// Reset clears the recorded state, ready for a new block. Reuses the
// underlying slice/map allocations.
func (r *ReadLogRecorder) Reset() {
	r.log = r.log[:0]
	r.keys = r.keys[:0]
	for k := range r.captured {
		delete(r.captured, k)
	}
	r.knownSeen = nil
	r.diverged = false
}

// MarkDiverged records that the execution behind this log is no longer a
// replay of the block's transactions alone: something read state and was
// then thrown away (a candidate that failed and was reverted, a reverted
// bundle), or the reads went around the recorder. Verifiers consume the log
// by position, so such a log must be rebuilt with ReplayRecorder before it
// is shipped. Marking is always safe; it only costs the rebuild.
func (r *ReadLogRecorder) MarkDiverged() { r.diverged = true }

// Diverged reports whether MarkDiverged was called since the last Reset.
func (r *ReadLogRecorder) Diverged() bool { return r.diverged }

// ReadAccountData captures one Account or AccountNotFound entry.
func (r *ReadLogRecorder) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	acc, err := r.inner.ReadAccountData(addr)
	if err != nil {
		return nil, err
	}
	r.keys = append(r.keys, readKey{addr: addr})
	if acc == nil {
		r.log = append(r.log, evmsdk.ReadLogAccountNotFound{})
		return nil, nil
	}
	bal := new(uint256.Int).Set(&acc.Balance)
	r.log = append(r.log, evmsdk.ReadLogAccount{
		Nonce:    acc.Nonce,
		Balance:  bal,
		CodeHash: acc.CodeHash,
	})
	return acc, nil
}

// ReadAccountStorage captures one Storage entry. Zero values are recorded
// with a nil Value to skip the uint256.Int allocation; EncodeReadLog
// treats nil as zero.
func (r *ReadLogRecorder) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	v, err := r.inner.ReadAccountStorage(addr, key)
	if err != nil {
		return nil, err
	}
	var val *uint256.Int
	if len(v) > 0 {
		val = new(uint256.Int).SetBytes(v)
	}
	r.log = append(r.log, evmsdk.ReadLogStorage{Value: val})
	r.keys = append(r.keys, readKey{addr: addr, slot: *key, storage: true})
	return v, nil
}

// ReadAccountCode captures the bytecode in the captured map (not the log).
//
// Skips emptyCodeHash and code hashes the consumer already knows about.
// The capture is idempotent — re-reading the same code hash within a
// block does not re-insert.
func (r *ReadLogRecorder) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	code, err := r.inner.ReadAccountCode(addr, codeHash)
	if err != nil {
		return nil, err
	}
	if codeHash == evmsdk.EmptyCodeHash || len(code) == 0 {
		return code, nil
	}
	if _, known := r.knownCodes[codeHash]; known {
		if _, already := r.knownSeen[codeHash]; !already {
			if r.knownSeen == nil {
				r.knownSeen = make(map[types.Hash][]byte)
			}
			r.knownSeen[codeHash] = append([]byte(nil), code...)
		}
		return code, nil
	}
	if _, already := r.captured[codeHash]; !already {
		cp := make([]byte, len(code))
		copy(cp, code)
		r.captured[codeHash] = cp
	}
	return code, nil
}

// ReadAccountCodeSize delegates to ReadAccountCode for capture, then
// returns the length. The extra map lookup is negligible relative to
// EVM work.
func (r *ReadLogRecorder) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// Compile-time interface check.
var _ state.StateReader = (*ReadLogRecorder)(nil)
