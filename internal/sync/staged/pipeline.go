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
// Pipeline executes a slice of Stage entries in order up to a target
// block. Tracks running state via atomic flags, persists per-stage
// progress and handles unwinding via unwindTo when a stage reports a
// reorg that requires earlier stages to rewind first.

package staged

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// Pipeline orchestrates the execution of stages in order.
type Pipeline struct {
	stages  []*Stage
	db      kv.RwDB
	running atomic.Bool

	// UnwindTo is set when a stage detects an issue requiring rewind.
	// The pipeline will unwind all stages to this point before continuing.
	mu       sync.Mutex
	unwindTo *uint64
}

// NewPipeline creates a new staged sync pipeline.
func NewPipeline(db kv.RwDB, stages []*Stage) *Pipeline {
	return &Pipeline{
		stages: stages,
		db:     db,
	}
}

// Run executes all stages in order until all are at the target block.
// It handles forward processing, unwinding on errors, and stage-level
// progress persistence.
func (p *Pipeline) Run(ctx context.Context, target uint64) error {
	if !p.running.CompareAndSwap(false, true) {
		return fmt.Errorf("pipeline already running")
	}
	defer p.running.Store(false)

	log.Info("Staged sync pipeline starting", "target", target, "stages", len(p.stages))

	// Forward pass: run each stage in order
	for _, stage := range p.stages {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Check for pending unwind
		p.mu.Lock()
		needsUnwind := p.unwindTo != nil
		var unwindTarget uint64
		if needsUnwind {
			unwindTarget = *p.unwindTo
		}
		p.mu.Unlock()
		if needsUnwind {
			return p.runUnwind(ctx, unwindTarget)
		}

		if err := p.runStage(ctx, stage, target); err != nil {
			return fmt.Errorf("stage %s failed: %w", stage.ID, err)
		}
	}

	// Handle unwind if one was requested during forward pass
	p.mu.Lock()
	needsUnwind := p.unwindTo != nil
	var unwindTarget uint64
	if needsUnwind {
		unwindTarget = *p.unwindTo
	}
	p.mu.Unlock()
	if needsUnwind {
		return p.runUnwind(ctx, unwindTarget)
	}

	log.Info("Staged sync pipeline completed", "target", target)
	return nil
}

// RequestUnwind marks that all stages should be unwound to the given block.
func (p *Pipeline) RequestUnwind(to uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unwindTo = &to
}

// runStage executes a single stage's Forward function.
func (p *Pipeline) runStage(ctx context.Context, stage *Stage, target uint64) error {
	if stage.Forward == nil {
		return nil
	}

	tx, err := p.db.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	progress, err := GetStageProgress(tx, stage.ID)
	if err != nil {
		return err
	}

	if progress >= target {
		log.Debug("Stage already at target", "stage", stage.ID, "progress", progress, "target", target)
		return nil
	}

	state := &StageState{
		ID:          stage.ID,
		BlockNumber: progress,
	}

	start := time.Now()
	log.Info("Stage starting", "stage", stage.ID, "from", progress, "to", target)

	newProgress, err := stage.Forward(ctx, state, tx, target)
	if err != nil {
		return err
	}

	if err := SaveStageProgress(tx, stage.ID, newProgress); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("Stage completed", "stage", stage.ID, "progress", newProgress, "elapsed", time.Since(start))
	return nil
}

// runUnwind reverts all stages to the unwind point, in reverse order.
func (p *Pipeline) runUnwind(ctx context.Context, to uint64) error {
	log.Warn("Pipeline unwinding", "to", to)
	defer func() { p.unwindTo = nil }()

	// Unwind in reverse stage order
	for i := len(p.stages) - 1; i >= 0; i-- {
		stage := p.stages[i]
		if stage.Unwind == nil {
			continue
		}

		tx, err := p.db.BeginRw(ctx)
		if err != nil {
			return err
		}

		progress, err := GetStageProgress(tx, stage.ID)
		if err != nil {
			tx.Rollback()
			return err
		}

		if progress <= to {
			tx.Rollback()
			continue
		}

		u := &UnwindState{
			ID:              stage.ID,
			UnwindPoint:     to,
			CurrentProgress: progress,
		}

		log.Info("Unwinding stage", "stage", stage.ID, "from", progress, "to", to)

		if err := stage.Unwind(ctx, u, tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("unwind stage %s: %w", stage.ID, err)
		}

		if err := SaveStageProgress(tx, stage.ID, to); err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

// Progress returns the current progress of each stage.
func (p *Pipeline) Progress() (map[SyncStage]uint64, error) {
	tx, err := p.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result := make(map[SyncStage]uint64, len(p.stages))
	for _, stage := range p.stages {
		progress, err := GetStageProgress(tx, stage.ID)
		if err != nil {
			return nil, err
		}
		result[stage.ID] = progress
	}
	return result, nil
}

// StageProgressTable is the MDBX table for persisting per-stage progress.
// Uses the existing "SyncStage" table defined in lib/kv/tables.go.
const StageProgressTable = "SyncStage"

// GetStageProgress reads the last completed block for a stage.
func GetStageProgress(tx kv.Getter, id SyncStage) (uint64, error) {
	v, err := tx.GetOne(StageProgressTable, stageProgressKey(id))
	if err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return 0, nil
	}
	if len(v) < 8 {
		return 0, fmt.Errorf("invalid stage progress data for %s: len=%d", id, len(v))
	}
	return binary.BigEndian.Uint64(v), nil
}

// SaveStageProgress persists the last completed block for a stage.
func SaveStageProgress(tx kv.Putter, id SyncStage, blockNum uint64) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], blockNum)
	return tx.Put(StageProgressTable, stageProgressKey(id), v[:])
}
