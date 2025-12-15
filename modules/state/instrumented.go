// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// =============================================================================
// Instrumented StateReader - wraps StateReader with timing metrics
// =============================================================================

// InstrumentedReader wraps a StateReader with timing instrumentation.
// Use this wrapper for debugging and performance analysis.
//
// Usage:
//
//	reader := NewInstrumentedReader(plainStateReader, true)
//	// ... use reader ...
//	reader.LogStats() // Print accumulated statistics
type InstrumentedReader struct {
	inner   StateReader
	enabled bool

	// Counters
	readAccountCount   uint64
	readStorageCount   uint64
	readCodeCount      uint64
	readCodeSizeCount  uint64
	readIncarnCount    uint64

	// Timing (nanoseconds)
	readAccountTime  uint64
	readStorageTime  uint64
	readCodeTime     uint64
	readCodeSizeTime uint64
	readIncarnTime   uint64
}

// NewInstrumentedReader creates a new instrumented reader wrapper.
// Set enabled=false in production to minimize overhead.
func NewInstrumentedReader(inner StateReader, enabled bool) *InstrumentedReader {
	return &InstrumentedReader{
		inner:   inner,
		enabled: enabled,
	}
}

func (r *InstrumentedReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if !r.enabled {
		return r.inner.ReadAccountData(address)
	}

	start := time.Now()
	result, err := r.inner.ReadAccountData(address)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&r.readAccountCount, 1)
	atomic.AddUint64(&r.readAccountTime, elapsed)

	return result, err
}

func (r *InstrumentedReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	if !r.enabled {
		return r.inner.ReadAccountStorage(address, incarnation, key)
	}

	start := time.Now()
	result, err := r.inner.ReadAccountStorage(address, incarnation, key)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&r.readStorageCount, 1)
	atomic.AddUint64(&r.readStorageTime, elapsed)

	return result, err
}

func (r *InstrumentedReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	if !r.enabled {
		return r.inner.ReadAccountCode(address, incarnation, codeHash)
	}

	start := time.Now()
	result, err := r.inner.ReadAccountCode(address, incarnation, codeHash)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&r.readCodeCount, 1)
	atomic.AddUint64(&r.readCodeTime, elapsed)

	return result, err
}

func (r *InstrumentedReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	if !r.enabled {
		return r.inner.ReadAccountCodeSize(address, incarnation, codeHash)
	}

	start := time.Now()
	result, err := r.inner.ReadAccountCodeSize(address, incarnation, codeHash)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&r.readCodeSizeCount, 1)
	atomic.AddUint64(&r.readCodeSizeTime, elapsed)

	return result, err
}

func (r *InstrumentedReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	if !r.enabled {
		return r.inner.ReadAccountIncarnation(address)
	}

	start := time.Now()
	result, err := r.inner.ReadAccountIncarnation(address)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&r.readIncarnCount, 1)
	atomic.AddUint64(&r.readIncarnTime, elapsed)

	return result, err
}

// Stats returns the accumulated statistics.
func (r *InstrumentedReader) Stats() ReaderStats {
	return ReaderStats{
		ReadAccountCount:  atomic.LoadUint64(&r.readAccountCount),
		ReadStorageCount:  atomic.LoadUint64(&r.readStorageCount),
		ReadCodeCount:     atomic.LoadUint64(&r.readCodeCount),
		ReadCodeSizeCount: atomic.LoadUint64(&r.readCodeSizeCount),
		ReadIncarnCount:   atomic.LoadUint64(&r.readIncarnCount),
		ReadAccountTime:   time.Duration(atomic.LoadUint64(&r.readAccountTime)),
		ReadStorageTime:   time.Duration(atomic.LoadUint64(&r.readStorageTime)),
		ReadCodeTime:      time.Duration(atomic.LoadUint64(&r.readCodeTime)),
		ReadCodeSizeTime:  time.Duration(atomic.LoadUint64(&r.readCodeSizeTime)),
		ReadIncarnTime:    time.Duration(atomic.LoadUint64(&r.readIncarnTime)),
	}
}

// LogStats logs the accumulated statistics at debug level.
func (r *InstrumentedReader) LogStats() {
	stats := r.Stats()
	log.Debug("StateReader stats",
		"account_reads", stats.ReadAccountCount,
		"account_time", stats.ReadAccountTime,
		"storage_reads", stats.ReadStorageCount,
		"storage_time", stats.ReadStorageTime,
		"code_reads", stats.ReadCodeCount,
		"code_time", stats.ReadCodeTime,
	)
}

// Reset clears all counters.
func (r *InstrumentedReader) Reset() {
	atomic.StoreUint64(&r.readAccountCount, 0)
	atomic.StoreUint64(&r.readStorageCount, 0)
	atomic.StoreUint64(&r.readCodeCount, 0)
	atomic.StoreUint64(&r.readCodeSizeCount, 0)
	atomic.StoreUint64(&r.readIncarnCount, 0)
	atomic.StoreUint64(&r.readAccountTime, 0)
	atomic.StoreUint64(&r.readStorageTime, 0)
	atomic.StoreUint64(&r.readCodeTime, 0)
	atomic.StoreUint64(&r.readCodeSizeTime, 0)
	atomic.StoreUint64(&r.readIncarnTime, 0)
}

// ReaderStats holds accumulated reader statistics.
type ReaderStats struct {
	ReadAccountCount  uint64
	ReadStorageCount  uint64
	ReadCodeCount     uint64
	ReadCodeSizeCount uint64
	ReadIncarnCount   uint64
	ReadAccountTime   time.Duration
	ReadStorageTime   time.Duration
	ReadCodeTime      time.Duration
	ReadCodeSizeTime  time.Duration
	ReadIncarnTime    time.Duration
}

// TotalReads returns the total number of read operations.
func (s ReaderStats) TotalReads() uint64 {
	return s.ReadAccountCount + s.ReadStorageCount + s.ReadCodeCount + s.ReadCodeSizeCount + s.ReadIncarnCount
}

// TotalTime returns the total time spent in read operations.
func (s ReaderStats) TotalTime() time.Duration {
	return s.ReadAccountTime + s.ReadStorageTime + s.ReadCodeTime + s.ReadCodeSizeTime + s.ReadIncarnTime
}

// =============================================================================
// Instrumented StateWriter - wraps StateWriter with timing metrics
// =============================================================================

// InstrumentedWriter wraps a StateWriter with timing instrumentation.
type InstrumentedWriter struct {
	inner   StateWriter
	enabled bool

	// Counters
	updateAccountCount uint64
	updateCodeCount    uint64
	deleteAccountCount uint64
	writeStorageCount  uint64
	createContractCount uint64

	// Timing (nanoseconds)
	updateAccountTime uint64
	updateCodeTime    uint64
	deleteAccountTime uint64
	writeStorageTime  uint64
	createContractTime uint64
}

// NewInstrumentedWriter creates a new instrumented writer wrapper.
func NewInstrumentedWriter(inner StateWriter, enabled bool) *InstrumentedWriter {
	return &InstrumentedWriter{
		inner:   inner,
		enabled: enabled,
	}
}

func (w *InstrumentedWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	if !w.enabled {
		return w.inner.UpdateAccountData(address, original, account)
	}

	start := time.Now()
	err := w.inner.UpdateAccountData(address, original, account)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&w.updateAccountCount, 1)
	atomic.AddUint64(&w.updateAccountTime, elapsed)

	return err
}

func (w *InstrumentedWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	if !w.enabled {
		return w.inner.UpdateAccountCode(address, incarnation, codeHash, code)
	}

	start := time.Now()
	err := w.inner.UpdateAccountCode(address, incarnation, codeHash, code)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&w.updateCodeCount, 1)
	atomic.AddUint64(&w.updateCodeTime, elapsed)

	return err
}

func (w *InstrumentedWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if !w.enabled {
		return w.inner.DeleteAccount(address, original)
	}

	start := time.Now()
	err := w.inner.DeleteAccount(address, original)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&w.deleteAccountCount, 1)
	atomic.AddUint64(&w.deleteAccountTime, elapsed)

	return err
}

func (w *InstrumentedWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if !w.enabled {
		return w.inner.WriteAccountStorage(address, incarnation, key, original, value)
	}

	start := time.Now()
	err := w.inner.WriteAccountStorage(address, incarnation, key, original, value)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&w.writeStorageCount, 1)
	atomic.AddUint64(&w.writeStorageTime, elapsed)

	return err
}

func (w *InstrumentedWriter) CreateContract(address types.Address) error {
	if !w.enabled {
		return w.inner.CreateContract(address)
	}

	start := time.Now()
	err := w.inner.CreateContract(address)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&w.createContractCount, 1)
	atomic.AddUint64(&w.createContractTime, elapsed)

	return err
}

// Stats returns the accumulated statistics.
func (w *InstrumentedWriter) Stats() WriterStats {
	return WriterStats{
		UpdateAccountCount:  atomic.LoadUint64(&w.updateAccountCount),
		UpdateCodeCount:     atomic.LoadUint64(&w.updateCodeCount),
		DeleteAccountCount:  atomic.LoadUint64(&w.deleteAccountCount),
		WriteStorageCount:   atomic.LoadUint64(&w.writeStorageCount),
		CreateContractCount: atomic.LoadUint64(&w.createContractCount),
		UpdateAccountTime:   time.Duration(atomic.LoadUint64(&w.updateAccountTime)),
		UpdateCodeTime:      time.Duration(atomic.LoadUint64(&w.updateCodeTime)),
		DeleteAccountTime:   time.Duration(atomic.LoadUint64(&w.deleteAccountTime)),
		WriteStorageTime:    time.Duration(atomic.LoadUint64(&w.writeStorageTime)),
		CreateContractTime:  time.Duration(atomic.LoadUint64(&w.createContractTime)),
	}
}

// LogStats logs the accumulated statistics at debug level.
func (w *InstrumentedWriter) LogStats() {
	stats := w.Stats()
	log.Debug("StateWriter stats",
		"account_updates", stats.UpdateAccountCount,
		"account_time", stats.UpdateAccountTime,
		"storage_writes", stats.WriteStorageCount,
		"storage_time", stats.WriteStorageTime,
		"code_updates", stats.UpdateCodeCount,
		"code_time", stats.UpdateCodeTime,
	)
}

// Reset clears all counters.
func (w *InstrumentedWriter) Reset() {
	atomic.StoreUint64(&w.updateAccountCount, 0)
	atomic.StoreUint64(&w.updateCodeCount, 0)
	atomic.StoreUint64(&w.deleteAccountCount, 0)
	atomic.StoreUint64(&w.writeStorageCount, 0)
	atomic.StoreUint64(&w.createContractCount, 0)
	atomic.StoreUint64(&w.updateAccountTime, 0)
	atomic.StoreUint64(&w.updateCodeTime, 0)
	atomic.StoreUint64(&w.deleteAccountTime, 0)
	atomic.StoreUint64(&w.writeStorageTime, 0)
	atomic.StoreUint64(&w.createContractTime, 0)
}

// WriterStats holds accumulated writer statistics.
type WriterStats struct {
	UpdateAccountCount  uint64
	UpdateCodeCount     uint64
	DeleteAccountCount  uint64
	WriteStorageCount   uint64
	CreateContractCount uint64
	UpdateAccountTime   time.Duration
	UpdateCodeTime      time.Duration
	DeleteAccountTime   time.Duration
	WriteStorageTime    time.Duration
	CreateContractTime  time.Duration
}

// TotalWrites returns the total number of write operations.
func (s WriterStats) TotalWrites() uint64 {
	return s.UpdateAccountCount + s.UpdateCodeCount + s.DeleteAccountCount + s.WriteStorageCount + s.CreateContractCount
}

// TotalTime returns the total time spent in write operations.
func (s WriterStats) TotalTime() time.Duration {
	return s.UpdateAccountTime + s.UpdateCodeTime + s.DeleteAccountTime + s.WriteStorageTime + s.CreateContractTime
}

// =============================================================================
// Compile-time interface checks
// =============================================================================

var (
	_ StateReader = (*InstrumentedReader)(nil)
	_ StateWriter = (*InstrumentedWriter)(nil)
)

