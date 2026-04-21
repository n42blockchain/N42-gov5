// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MVStateView: per-tx read/write view over an MVHashMap + base store.
// Layered reads record every observation as a ReadRecord so the
// Block-STM validator can check whether a concurrent writer invalidated
// what this tx saw.
//
// This file is part of the Phase 2 parallel EVM PoC. It is NOT yet
// wired into IntraBlockState or EVM; the interfaces here are the
// MINIMUM needed for a Block-STM scheduler + worker to round-trip
// through a state-like API. Phase 3 will retrofit it behind the full
// StateReader / StateWriter interfaces.
//
// Invariants:
//   - A view belongs to a single (txIdx, incarnation) pair. Starting a
//     new incarnation of the same tx means creating a fresh view.
//   - Reads may return values produced by an earlier tx in the same
//     block (via MVHashMap), or fall through to the base store if no
//     earlier tx wrote that key.
//   - Writes go to an in-memory writeSet keyed by string(key). Writes
//     are committed to MVHashMap by FlushWrites(). Aborts clear the
//     writeSet without touching MVHashMap.
//   - The view is NOT safe for concurrent use by multiple goroutines;
//     each worker owns its view exclusively.

package state

import (
	"sync"
)

// MVBaseReader is the fallback layer consulted when MVHashMap has no
// entry visible to the reader's txIdx. In production this is
// MDBX + PlainStateBuffer; for tests it can be a plain map.
type MVBaseReader interface {
	// Get returns the current committed value for key, or nil if
	// absent. Implementations must be concurrency-safe: multiple
	// MVStateView instances read in parallel from the same base.
	Get(key []byte) ([]byte, error)
}

// ReadSource identifies where a read's result came from.
type ReadSource uint8

const (
	// ReadFromMV: the value came from MVHashMap (an earlier tx's write).
	ReadFromMV ReadSource = iota
	// ReadFromBase: the value came from the underlying base store
	// (MDBX / PlainStateBuffer). No tx in the current block had written
	// this key before our reader.
	ReadFromBase
)

// ReadRecord captures one read's observation for later validation.
type ReadRecord struct {
	Key      []byte
	Source   ReadSource
	// When Source == ReadFromMV: WriterVer identifies which earlier
	// tx produced the value we read.
	WriterVer Version
	// Value actually returned to the caller. Stored so callers can
	// distinguish nil (absent) from empty ([]byte{}); also useful for
	// debug logs.
	Value []byte
}

// MVStateView is one tx's view of MVHashMap + base store for a specific
// (txIdx, incarnation).
type MVStateView struct {
	mv          *MVHashMap
	base        MVBaseReader
	txIdx       int
	incarnation uint32

	// readSet lists every read this tx made, in order. Duplicates
	// (same key read twice) appear twice; validation iterates all and
	// re-checks each.
	readSet []ReadRecord

	// writeSet buffers this tx's writes. Values are materialized into
	// MVHashMap by FlushWrites; on Abort, they are discarded. Map
	// iteration order is randomized, but FlushWrites sorts by key to
	// preserve determinism on the MVHashMap side.
	writeSet map[string][]byte

	// sawEstimate is set to true if any read observed an MVEstimate
	// entry. Such a read cannot produce a valid result; the executor
	// should abort and let the scheduler re-execute after the blocking
	// writer finishes.
	sawEstimate   bool
	estimatedOn   Version // the writer we blocked on (for diagnostics)
}

// NewMVStateView creates a view for (txIdx, incarnation). Caller must
// provide a base reader (MDBX + PlainStateBuffer in production).
func NewMVStateView(mv *MVHashMap, base MVBaseReader, txIdx int, incarnation uint32) *MVStateView {
	return &MVStateView{
		mv:          mv,
		base:        base,
		txIdx:       txIdx,
		incarnation: incarnation,
		readSet:     make([]ReadRecord, 0, 16),
		writeSet:    make(map[string][]byte, 16),
	}
}

// Get returns the value for key visible to this tx. Records the read
// in the readSet for later validation. Returns (value, nil) on success.
//
// If the nearest earlier writer's entry in MVHashMap is marked
// ESTIMATE (that writer is re-executing), the view records the dependency
// via sawEstimate and returns (nil, nil) — the caller should check
// AbortPending() after the read and, if true, halt speculative
// execution.
func (v *MVStateView) Get(key []byte) ([]byte, error) {
	// Self-read: if this tx wrote key earlier in its own execution,
	// see its own write (not in the readSet; tx-local visibility).
	if val, ok := v.writeSet[string(key)]; ok {
		return val, nil
	}
	// Query MVHashMap for highest committed earlier write.
	mvVal, ver, status := v.mv.Read(key, v.txIdx)
	switch status {
	case MVOk:
		v.readSet = append(v.readSet, ReadRecord{
			Key:       append([]byte(nil), key...),
			Source:    ReadFromMV,
			WriterVer: ver,
			Value:     mvVal,
		})
		return mvVal, nil
	case MVEstimate:
		v.sawEstimate = true
		v.estimatedOn = ver
		return nil, nil
	case MVNotFound:
		// Fall through to base store.
		baseVal, err := v.base.Get(key)
		if err != nil {
			return nil, err
		}
		v.readSet = append(v.readSet, ReadRecord{
			Key:    append([]byte(nil), key...),
			Source: ReadFromBase,
			Value:  baseVal,
		})
		return baseVal, nil
	}
	return nil, nil
}

// Set writes value to key in this tx's buffer. The write becomes visible
// to later txs only after FlushWrites is called (which happens at the
// end of a successful Execute task).
func (v *MVStateView) Set(key []byte, value []byte) {
	v.writeSet[string(key)] = append([]byte(nil), value...)
}

// AbortPending reports whether any read during this execution observed
// an MVEstimate, requiring the executor to abort and let the scheduler
// re-run this tx after the blocking writer settles.
func (v *MVStateView) AbortPending() bool {
	return v.sawEstimate
}

// BlockingWriter returns the (txIdx, incarnation) of the writer whose
// estimate-entry caused the abort, for scheduler diagnostics. Only
// meaningful when AbortPending() is true.
func (v *MVStateView) BlockingWriter() Version {
	return v.estimatedOn
}

// ReadSet returns the captured reads for validation. Callers must not
// mutate the returned slice.
func (v *MVStateView) ReadSet() []ReadRecord {
	return v.readSet
}

// WriteSet returns the captured writes. Map iteration order is random;
// callers that need determinism should sort by key.
func (v *MVStateView) WriteSet() map[string][]byte {
	return v.writeSet
}

// FlushWrites pushes writeSet to MVHashMap under this view's (txIdx,
// incarnation). Called at the end of a successful Execute task.
// Returns the list of keys written, for scheduler bookkeeping.
func (v *MVStateView) FlushWrites() []string {
	ver := Version{TxIdx: v.txIdx, Incarnation: v.incarnation}
	keys := make([]string, 0, len(v.writeSet))
	for k, val := range v.writeSet {
		v.mv.Write([]byte(k), ver, val)
		keys = append(keys, k)
	}
	return keys
}

// DropWrites removes this tx's writes from MVHashMap. Called on abort
// when the new incarnation will write a different set (old entries for
// keys this incarnation DOESN'T write must not remain visible).
func (v *MVStateView) DropWrites(prevKeys []string) {
	for _, k := range prevKeys {
		v.mv.Delete([]byte(k), v.txIdx)
	}
}

// MarkWritesEstimate flags this tx's writes as estimates so concurrent
// readers observe MVEstimate and abort. Called by the scheduler when
// this tx is about to re-execute.
func (v *MVStateView) MarkWritesEstimate(prevKeys []string) {
	for _, k := range prevKeys {
		v.mv.MarkEstimate([]byte(k), v.txIdx)
	}
}

// Validate re-checks every read against the current MVHashMap state.
// Returns true if ALL reads still produce the same observation (same
// source and writer version). Returns false if anything changed,
// meaning this tx's speculative result is stale and must re-execute.
//
// Validate is safe to call from the scheduler's Validate worker; it
// does not mutate the view.
func (v *MVStateView) Validate() bool {
	for _, rec := range v.readSet {
		_, ver, status := v.mv.Read(rec.Key, v.txIdx)
		switch rec.Source {
		case ReadFromMV:
			if status != MVOk {
				return false
			}
			if ver.TxIdx != rec.WriterVer.TxIdx ||
				ver.Incarnation != rec.WriterVer.Incarnation {
				return false
			}
		case ReadFromBase:
			if status != MVNotFound {
				// An earlier tx committed to this key since we read;
				// our base-read is invalidated.
				return false
			}
		}
	}
	return true
}

// --- MapBaseReader: simple MVBaseReader for tests ---

// MapBaseReader wraps a plain map as an MVBaseReader. Safe for
// concurrent reads (mutex-protected). Intended for unit tests; the
// production base will be MDBX + PlainStateBuffer wrapped in an adapter.
type MapBaseReader struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMapBaseReader(initial map[string][]byte) *MapBaseReader {
	m := &MapBaseReader{data: make(map[string][]byte, len(initial))}
	for k, v := range initial {
		m.data[k] = append([]byte(nil), v...)
	}
	return m
}

func (m *MapBaseReader) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

// Put is a test helper; production base stores don't expose writes
// through this interface.
func (m *MapBaseReader) Put(key []byte, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = append([]byte(nil), value...)
}
