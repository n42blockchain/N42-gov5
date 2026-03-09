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

package peerdas

import (
	"context"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// Config holds PeerDAS configuration. Disabled by default.
type Config struct {
	Enable          bool   `json:"peerdas" yaml:"peerdas"`
	CustodyCount    uint64 `json:"custody_count" yaml:"custody_count"`     // columns to custody; default: CustodyRequirement
	SamplingEnabled bool   `json:"das_sampling" yaml:"das_sampling"`       // whether to perform DAS on new blocks
	SampleCount     int    `json:"das_sample_count" yaml:"das_sample_count"` // columns per sample; default: SamplesPerSlot
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

// Service manages PeerDAS data column custody, storage, and sampling.
type Service struct {
	cfg         Config
	db          kv.RwDB
	custodyCols []uint64 // sorted column indices this node custodies
	nodeID      []byte

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

// NewService creates a new PeerDAS service. If cfg.Enable is false the
// returned service is a no-op stub; all methods return immediately.
func NewService(cfg Config, nodeID []byte, db kv.RwDB) *Service {
	cfg.Validate()

	var cols []uint64
	if cfg.Enable && len(nodeID) > 0 {
		cols = CustodyColumns(nodeID, cfg.CustodyCount)
	}

	return &Service{
		cfg:         cfg,
		db:          db,
		nodeID:      nodeID,
		custodyCols: cols,
	}
}

// Start begins the PeerDAS background service. Currently a placeholder
// for future gossip subscription and sampling loops.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enable {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return ErrServiceAlreadyRunning
	}

	// Store the derived context for future background goroutines (e.g., sampling loops).
	ctx, cancel := context.WithCancel(ctx)
	_ = ctx // will be used when background loops are added
	s.cancel = cancel
	s.running = true

	log.Info("PeerDAS service started",
		"custody_columns", len(s.custodyCols),
		"sampling_enabled", s.cfg.SamplingEnabled,
	)

	return nil
}

// Stop shuts down the PeerDAS service.
func (s *Service) Stop() error {
	if !s.cfg.Enable {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}
	s.running = false

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
// all sampled columns are available in local storage. Returns true if all
// sampled columns are present, false otherwise.
//
// This is a simplified check: KZG proof verification is stubbed to a
// length-only check. Full erasure-coding verification will be added later.
func (s *Service) VerifyAvailability(ctx context.Context, blockHash types.Hash, blockNum uint64) (bool, error) {
	if !s.cfg.Enable {
		return false, ErrServiceNotEnabled
	}

	sampleCols := SampleColumns(blockHash, s.cfg.SampleCount)

	var available bool
	err := s.db.View(ctx, func(tx kv.Tx) error {
		for _, colIdx := range sampleCols {
			col, err := GetColumn(tx, blockHash, colIdx)
			if err != nil {
				return err
			}
			if col == nil {
				available = false
				return nil
			}
			// Stub KZG verification: just check proof is non-empty.
			if len(col.KZGProof) == 0 {
				available = false
				return nil
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

// StoreDataColumn validates and persists a data column. Only columns that
// this node custodies (or all columns if custody is not relevant) are stored.
func (s *Service) StoreDataColumn(ctx context.Context, col *DataColumn) error {
	if !s.cfg.Enable {
		return ErrServiceNotEnabled
	}

	if err := col.Validate(); err != nil {
		return err
	}

	return s.db.Update(ctx, func(tx kv.RwTx) error {
		return StoreColumn(tx, col)
	})
}
