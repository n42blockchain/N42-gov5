// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// HeaderLookup resolves a block hash to its number, reporting whether
// the block is known locally. The window manager refuses to open a
// collection window for unknown blocks — receipts for arbitrary hashes
// cannot make the node allocate anything.
type HeaderLookup func(hash types.Hash) (number uint64, ok bool)

// maxOpenWindows bounds concurrently collecting blocks. At one block
// per ~2s and a sub-minute window, a healthy chain keeps <64 open;
// the cap is DoS depth, not a tunable.
const maxOpenWindows = 128

var (
	// ErrUnknownBlock reports a receipt for a block this node does not have.
	ErrUnknownBlock = errors.New("mobileverify: unknown block")
	// ErrTooManyWindows reports the open-window cap.
	ErrTooManyWindows = errors.New("mobileverify: too many open collection windows")
)

// WindowManager owns the receipt intake lifecycle (design §5c-§6):
// lazily opens a per-block Collector on the first receipt for a known
// block, closes it after the configured window, and hands the resulting
// certificates to the CertStore. Divergent cohorts surface via a Warn
// log — the phase-6 alarm pipeline replaces that with a real signal.
type WindowManager struct {
	reg    *Registry
	lookup HeaderLookup
	window time.Duration
	certs  *CertStore

	mu      sync.Mutex
	open    map[types.Hash]*Collector
	timers  map[types.Hash]*time.Timer
	stopped bool
}

// NewWindowManager creates a manager; window is the collection span
// from the first receipt for a block.
func NewWindowManager(reg *Registry, lookup HeaderLookup, window time.Duration, certs *CertStore) *WindowManager {
	if window <= 0 {
		window = 45 * time.Second
	}
	return &WindowManager{
		reg:    reg,
		lookup: lookup,
		window: window,
		certs:  certs,
		open:   make(map[types.Hash]*Collector),
		timers: make(map[types.Hash]*time.Timer),
	}
}

// Submit routes one receipt into its block's collection window,
// opening the window if this is the block's first receipt.
func (m *WindowManager) Submit(r *Receipt) (MobileIndex, error) {
	if r == nil {
		return 0, errors.New("mobileverify: nil receipt")
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return 0, ErrWindowClosed
	}
	col, ok := m.open[r.BlockHash]
	if !ok {
		number, known := m.lookup(r.BlockHash)
		if !known {
			m.mu.Unlock()
			return 0, ErrUnknownBlock
		}
		if number != r.BlockNumber {
			m.mu.Unlock()
			return 0, fmt.Errorf("%w: receipt number %d, block is %d", ErrWrongBlock, r.BlockNumber, number)
		}
		if len(m.open) >= maxOpenWindows {
			m.mu.Unlock()
			return 0, ErrTooManyWindows
		}
		col = NewCollector(m.reg, r.BlockHash, r.BlockNumber)
		m.open[r.BlockHash] = col
		hash := r.BlockHash
		m.timers[hash] = time.AfterFunc(m.window, func() { m.closeWindow(hash) })
	}
	m.mu.Unlock()
	return col.Add(r)
}

func (m *WindowManager) closeWindow(hash types.Hash) {
	m.mu.Lock()
	col, ok := m.open[hash]
	delete(m.open, hash)
	delete(m.timers, hash)
	m.mu.Unlock()
	if !ok {
		return
	}
	certs, err := col.Close(NowMs())
	if err != nil || len(certs) == 0 {
		return
	}
	m.certs.Put(certs)
	if len(certs) > 1 {
		// More than one cohort = at least one set of registered devices
		// re-executed this block to a DIFFERENT receipts root. This is the
		// signal the pipeline exists for.
		log.Warn("mobileverify: divergent attestation cohorts",
			"block", certs[0].BlockNumber, "hash", hash.Hex()[:12], "cohorts", len(certs))
	}
	log.Debug("mobileverify: window closed",
		"block", certs[0].BlockNumber, "cohorts", len(certs))
}

// Stop closes every open window immediately (certs still aggregate) and
// rejects further submissions.
func (m *WindowManager) Stop() {
	m.mu.Lock()
	m.stopped = true
	hashes := make([]types.Hash, 0, len(m.open))
	for h, t := range m.timers {
		t.Stop()
		hashes = append(hashes, h)
	}
	m.mu.Unlock()
	for _, h := range hashes {
		m.closeWindow(h)
	}
}

// OpenWindows returns the number of currently collecting blocks.
func (m *WindowManager) OpenWindows() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

// CertStore retains the most recent blocks' certificates, keyed by
// block hash, with insertion-order eviction.
type CertStore struct {
	mu     sync.RWMutex
	max    int
	byHash map[types.Hash][]*MobileAttestationCert
	order  []types.Hash
}

// NewCertStore creates a store retaining certs for up to max blocks.
func NewCertStore(max int) *CertStore {
	if max <= 0 {
		max = 512
	}
	return &CertStore{max: max, byHash: make(map[types.Hash][]*MobileAttestationCert)}
}

// Put stores one block's certificates (all cohorts share a block hash).
func (s *CertStore) Put(certs []*MobileAttestationCert) {
	if len(certs) == 0 {
		return
	}
	hash := certs[0].BlockHash
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byHash[hash]; !exists {
		s.order = append(s.order, hash)
		if len(s.order) > s.max {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.byHash, oldest)
		}
	}
	s.byHash[hash] = certs
}

// Get returns a block's certificates (majority cohort first).
func (s *CertStore) Get(blockHash types.Hash) []*MobileAttestationCert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byHash[blockHash]
}

// Len returns the number of blocks with stored certificates.
func (s *CertStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byHash)
}
