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

// Package staged implements an Erigon/reth-inspired staged sync pipeline.
//
// Each stage is an independent unit that can:
//   - Forward: process blocks from its last checkpoint to a target
//   - Unwind: revert state to a previous block (using changesets)
//   - Prune: remove old data below a retention threshold
//
// The pipeline orchestrates stages in order, persisting per-stage progress
// to the database so sync can resume after restart.
package staged

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
)

// SyncStage identifies a named stage in the pipeline.
type SyncStage string

// Predefined stages in execution order.
const (
	StageHeaders    SyncStage = "Headers"
	StageBodies     SyncStage = "Bodies"
	StageSenders    SyncStage = "Senders"
	StageExecution  SyncStage = "Execution"
	StageHashState  SyncStage = "HashState"
	StageCommitment SyncStage = "Commitment"
	StageFinish     SyncStage = "Finish"
)

// StageState tracks the progress of a single stage.
type StageState struct {
	// ID is the stage identifier.
	ID SyncStage
	// BlockNumber is the last successfully processed block.
	BlockNumber uint64
}

// UnwindState holds the parameters for a stage unwind operation.
type UnwindState struct {
	// ID is the stage being unwound.
	ID SyncStage
	// UnwindPoint is the block number to revert to.
	UnwindPoint uint64
	// CurrentProgress is the block the stage was at before unwinding.
	CurrentProgress uint64
}

// PruneState holds the parameters for a stage prune operation.
type PruneState struct {
	// ID is the stage being pruned.
	ID SyncStage
	// PruneTo is the oldest block to keep.
	PruneTo uint64
}

// Stage defines the interface that each sync stage must implement.
type Stage struct {
	// ID uniquely identifies this stage.
	ID SyncStage
	// Description is a human-readable description.
	Description string
	// Forward advances the stage from its last checkpoint toward the target.
	Forward ForwardFunc
	// Unwind reverts the stage to the given unwind point.
	Unwind UnwindFunc
	// Prune removes old data below the prune threshold.
	Prune PruneFunc
}

// ForwardFunc processes blocks from the stage's last checkpoint to the target.
// Returns the new progress block number.
type ForwardFunc func(ctx context.Context, s *StageState, tx kv.RwTx, target uint64) (uint64, error)

// UnwindFunc reverts the stage to a prior block number.
type UnwindFunc func(ctx context.Context, u *UnwindState, tx kv.RwTx) error

// PruneFunc removes old data below the prune threshold.
type PruneFunc func(ctx context.Context, p *PruneState, tx kv.RwTx) error

// stageProgressKey returns the database key for persisting stage progress.
func stageProgressKey(id SyncStage) []byte {
	return []byte(fmt.Sprintf("SyncStage-%s", id))
}
