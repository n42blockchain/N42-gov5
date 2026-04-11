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
//
// PeerDAS Service and Config. The Service owns the KZG Verifier, MDBX
// column Store and custody scheduler; lifecycle hooks start a
// background sampler loop that periodically picks SamplesPerSlot
// columns, fetches them via the P2P custody protocol and checks
// availability. Disabled by default through Config.Enabled.

package peerdas

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// Config holds PeerDAS configuration. Disabled by default.
type Config struct {
	Enable          bool   `json:"peerdas" yaml:"peerdas"`
	CustodyCount    uint64 `json:"custody_count" yaml:"custody_count"`       // columns to custody; default: CustodyRequirement
	SamplingEnabled bool   `json:"das_sampling" yaml:"das_sampling"`         // whether to perform DAS on new blocks
	SampleCount     int    `json:"das_sample_count" yaml:"das_sample_count"` // columns per sample; default: SamplesPerSlot

	// SkipKZGVerification disables cryptographic KZG verification when storing
	// or verifying data columns. Only use for testing/development.
	SkipKZGVerification bool `json:"skip_kzg_verification" yaml:"skip_kzg_verification"`
}

// DefaultConfig returns a Config with sensible defaults (disabled).
func DefaultConfig() Config {
	return Config{
		Enable:          false,
		CustodyCount:    CustodyRequirement,
		SamplingEnabled: false,
		SampleCount:     SamplesPerSlot,
	}
}

// Validate checks configuration values and fills in defaults.
func (c *Config) Validate() {
	if c.CustodyCount == 0 || c.CustodyCount < CustodyRequirement {
		c.CustodyCount = CustodyRequirement
	}
	if c.CustodyCount > NumberOfColumns {
		c.CustodyCount = NumberOfColumns
	}
	if c.SampleCount <= 0 {
		c.SampleCount = SamplesPerSlot
	}
	if c.SampleCount > NumberOfColumns {
		c.SampleCount = NumberOfColumns
	}
}

// BlockProvider supplies the service with the latest block information
// for periodic sampling. Implementations typically wrap the blockchain.
type BlockProvider interface {
	// CurrentBlock returns the hash and number of the current head block.
	CurrentBlock() (types.Hash, uint64)
}

// Service manages PeerDAS data column custody, storage, and sampling.
type Service struct {
	cfg         Config
	db          kv.RwDB
	custodyCols []uint64 // sorted column indices this node custodies
	nodeID      []byte
	verifier    *Verifier // nil if KZG verification is disabled or init fails

	blockProvider    BlockProvider
	lastSampledBlock atomic.Uint64 // block number of the last completed sampling round

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup // tracks background goroutines
}

// NewService creates a new PeerDAS service. If cfg.Enable is false the
// returned service is a no-op stub; all methods return immediately.
func NewService(cfg Config, nodeID []byte, db kv.RwDB) *Service {
	cfg.Validate()

	var cols []uint64
	if cfg.Enable && len(nodeID) > 0 {
		cols = CustodyColumns(nodeID, cfg.CustodyCount)
	}

	s := &Service{
		cfg:         cfg,
		db:          db,
		nodeID:      nodeID,
		custodyCols: cols,
	}

	// Initialize KZG verifier if enabled and verification is not skipped.
	if cfg.Enable && !cfg.SkipKZGVerification {
		v, err := NewVerifier()
		if err != nil {
			log.Warn("PeerDAS KZG verifier init failed, falling back to structural checks",
				"err", err)
		} else {
			s.verifier = v
		}
	}

	return s
}

// SetBlockProvider sets the block provider used by the sampling loop.
// Must be called before Start.
func (s *Service) SetBlockProvider(bp BlockProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockProvider = bp
}

// Start begins the PeerDAS background service. When sampling is enabled
// and a BlockProvider is set, a background goroutine periodically checks
// data column availability for the latest blocks.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enable {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return ErrServiceAlreadyRunning
	}

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true

	log.Info("PeerDAS service started",
		"custody_columns", len(s.custodyCols),
		"sampling_enabled", s.cfg.SamplingEnabled,
		"kzg_verification", s.verifier != nil,
	)

	if s.cfg.SamplingEnabled && s.blockProvider != nil {
		s.wg.Add(1)
		go s.samplingLoop(ctx)
	}

	return nil
}

// samplingLoop periodically samples columns for new blocks and logs
// the availability results. It runs until the context is cancelled.
func (s *Service) samplingLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Duration(SamplingInterval) * time.Second)
	defer ticker.Stop()

	log.Info("PeerDAS sampling loop started",
		"interval_sec", SamplingInterval,
		"sample_count", s.cfg.SampleCount,
	)

	for {
		select {
		case <-ctx.Done():
			log.Info("PeerDAS sampling loop stopped")
			return
		case <-ticker.C:
			s.runSamplingRound(ctx)
		}
	}
}

// runSamplingRound performs a single sampling check on the current head block.
// Skips if the block has already been sampled.
func (s *Service) runSamplingRound(ctx context.Context) {
	s.mu.Lock()
	bp := s.blockProvider
	s.mu.Unlock()
	if bp == nil {
		return
	}
	blockHash, blockNum := bp.CurrentBlock()
	if blockHash == (types.Hash{}) || blockNum == 0 {
		return
	}

	// Skip blocks we have already sampled.
	if blockNum <= s.lastSampledBlock.Load() {
		return
	}

	available, err := s.VerifyAvailability(ctx, blockHash, blockNum)
	if err != nil {
		log.Warn("PeerDAS sampling failed",
			"block", blockNum,
			"hash", blockHash,
			"err", err,
		)
		return
	}

	s.lastSampledBlock.Store(blockNum)

	if available {
		log.Debug("PeerDAS sampling passed",
			"block", blockNum,
			"hash", blockHash,
			"samples", s.cfg.SampleCount,
		)
	} else {
		log.Warn("PeerDAS sampling: data unavailable",
			"block", blockNum,
			"hash", blockHash,
			"samples", s.cfg.SampleCount,
		)
	}
}

// Stop shuts down the PeerDAS service and waits for background goroutines.
func (s *Service) Stop() error {
	if !s.cfg.Enable {
		return nil
	}

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	s.mu.Unlock()

	// Wait for background goroutines (sampling loop) to finish.
	s.wg.Wait()

	log.Info("PeerDAS service stopped")
	return nil
}

// IsCustodyColumn returns true if this node custodies the given column index.
func (s *Service) IsCustodyColumn(colIdx uint64) bool {
	for _, c := range s.custodyCols {
		if c == colIdx {
			return true
		}
	}
	return false
}

// CustodyColumnIndices returns a copy of this node's custody column list.
func (s *Service) CustodyColumnIndices() []uint64 {
	result := make([]uint64, len(s.custodyCols))
	copy(result, s.custodyCols)
	return result
}

// VerifyAvailability samples columns for the given block and checks that
// all sampled columns are available in local storage. When a KZG verifier
// is configured, it additionally performs cryptographic proof verification.
// Returns true if all sampled columns are present and valid.
func (s *Service) VerifyAvailability(ctx context.Context, blockHash types.Hash, blockNum uint64) (bool, error) {
	if !s.cfg.Enable {
		return false, ErrServiceNotEnabled
	}

	sampleCols := SampleColumns(blockHash, s.cfg.SampleCount)

	var available bool
	err := s.db.View(ctx, func(tx kv.Tx) error {
		for _, colIdx := range sampleCols {
			if colIdx >= NumberOfColumns {
				available = false
				return nil
			}

			col, err := GetColumn(tx, blockHash, colIdx)
			if err != nil {
				return err
			}
			if col == nil {
				available = false
				return nil
			}

			// Structural validation.
			if err := VerifyDataColumnLight(col); err != nil {
				available = false
				return nil
			}

			// Cryptographic KZG verification when verifier is available.
			if s.verifier != nil {
				if err := s.verifier.VerifyDataColumn(col); err != nil {
					log.Warn("PeerDAS KZG verification failed",
						"block", blockNum,
						"column", colIdx,
						"err", err,
					)
					available = false
					return nil
				}
			}
		}
		available = true
		return nil
	})
	if err != nil {
		return false, err
	}

	return available, nil
}

// StoreDataColumn validates and persists a data column. When a KZG verifier
// is configured, the column's KZG proofs are verified before storage.
func (s *Service) StoreDataColumn(ctx context.Context, col *DataColumn) error {
	if !s.cfg.Enable {
		return ErrServiceNotEnabled
	}

	if err := col.Validate(); err != nil {
		return err
	}

	// Cryptographic verification before storing.
	if s.verifier != nil {
		if err := s.verifier.VerifyDataColumn(col); err != nil {
			return err
		}
	}

	return s.db.Update(ctx, func(tx kv.RwTx) error {
		return StoreColumn(tx, col)
	})
}
