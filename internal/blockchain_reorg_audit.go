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

package internal

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// =============================================================================
// Reorg Audit System
// =============================================================================

// ReorgAuditConfig holds configuration for reorg auditing.
type ReorgAuditConfig struct {
	// Enable enables reorg audit logging
	Enable bool

	// DetailedLogs enables detailed per-block logging during reorg
	DetailedLogs bool

	// WarnDepth is the reorg depth that triggers a warning
	WarnDepth int

	// CriticalDepth is the reorg depth that triggers a critical alert
	CriticalDepth int

	// ValidateStateRoot enables state root validation before and after reorg
	ValidateStateRoot bool
}

// DefaultReorgAuditConfig returns the default audit configuration.
func DefaultReorgAuditConfig() *ReorgAuditConfig {
	return &ReorgAuditConfig{
		Enable:            true,
		DetailedLogs:      false,
		WarnDepth:         5,
		CriticalDepth:     10,
		ValidateStateRoot: true,
	}
}

// ReorgAudit tracks reorg events and provides audit logging.
type ReorgAudit struct {
	config *ReorgAuditConfig

	// Statistics
	totalReorgs    atomic.Uint64
	maxDepthSeen   atomic.Uint64
	lastReorgTime  atomic.Int64
	reorgsByDepth  sync.Map // depth -> count

	// Hooks
	onReorgStart  func(audit *ReorgEvent)
	onReorgEnd    func(audit *ReorgEvent)
}

// ReorgEvent represents a single reorg event.
type ReorgEvent struct {
	// Timing
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration

	// Chain info
	OldHead       block.IBlock
	NewHead       block.IBlock
	CommonBlock   block.IBlock
	Depth         int
	OldChainLen   int
	NewChainLen   int

	// State
	OldStateRoot  types.Hash
	NewStateRoot  types.Hash
	StateRootOK   bool

	// Transactions
	DeletedTxs    int
	AddedTxs      int

	// Status
	Success       bool
	Error         error
}

// NewReorgAudit creates a new ReorgAudit instance.
func NewReorgAudit(config *ReorgAuditConfig) *ReorgAudit {
	if config == nil {
		config = DefaultReorgAuditConfig()
	}
	return &ReorgAudit{
		config: config,
	}
}

// SetOnReorgStart sets the callback for reorg start.
func (ra *ReorgAudit) SetOnReorgStart(fn func(*ReorgEvent)) {
	ra.onReorgStart = fn
}

// SetOnReorgEnd sets the callback for reorg end.
func (ra *ReorgAudit) SetOnReorgEnd(fn func(*ReorgEvent)) {
	ra.onReorgEnd = fn
}

// StartReorg creates a new ReorgEvent for tracking.
func (ra *ReorgAudit) StartReorg(oldHead, newHead block.IBlock) *ReorgEvent {
	event := &ReorgEvent{
		StartTime: time.Now(),
		OldHead:   oldHead,
		NewHead:   newHead,
	}

	if oldHead != nil {
		event.OldStateRoot = oldHead.StateRoot()
	}

	if ra.config.Enable {
		log.Info("Reorg audit: starting",
			"old_head", formatBlockInfo(oldHead),
			"new_head", formatBlockInfo(newHead),
		)
	}

	if ra.onReorgStart != nil {
		ra.onReorgStart(event)
	}

	return event
}

// EndReorg finalizes a ReorgEvent and logs the results.
func (ra *ReorgAudit) EndReorg(event *ReorgEvent, commonBlock block.IBlock, oldChain, newChain []block.IBlock, deletedTxs, addedTxs int, err error) {
	event.EndTime = time.Now()
	event.Duration = event.EndTime.Sub(event.StartTime)
	event.CommonBlock = commonBlock
	event.OldChainLen = len(oldChain)
	event.NewChainLen = len(newChain)
	event.DeletedTxs = deletedTxs
	event.AddedTxs = addedTxs
	event.Error = err
	event.Success = err == nil

	// Calculate depth
	if event.OldHead != nil && commonBlock != nil {
		oldNum := event.OldHead.Number64()
		commonNum := commonBlock.Number64()
		if oldNum != nil && commonNum != nil {
			event.Depth = int(oldNum.Uint64() - commonNum.Uint64())
		}
	}

	// Update state root
	if event.NewHead != nil {
		event.NewStateRoot = event.NewHead.StateRoot()
	}

	// Validate state root if enabled
	if ra.config.ValidateStateRoot && event.Success {
		// State root validation - check that old and new roots are different
		// (a successful reorg should change the state root)
		event.StateRootOK = event.OldStateRoot != event.NewStateRoot || event.Depth == 0
	}

	// Update statistics
	ra.totalReorgs.Add(1)
	ra.lastReorgTime.Store(event.EndTime.Unix())
	
	// Track max depth
	for {
		old := ra.maxDepthSeen.Load()
		if uint64(event.Depth) <= old {
			break
		}
		if ra.maxDepthSeen.CompareAndSwap(old, uint64(event.Depth)) {
			break
		}
	}

	// Track reorgs by depth
	val, _ := ra.reorgsByDepth.LoadOrStore(event.Depth, new(atomic.Uint64))
	val.(*atomic.Uint64).Add(1)

	// Log based on severity
	ra.logReorgEvent(event)

	if ra.onReorgEnd != nil {
		ra.onReorgEnd(event)
	}
}

// logReorgEvent logs the reorg event with appropriate severity.
func (ra *ReorgAudit) logReorgEvent(event *ReorgEvent) {
	if !ra.config.Enable {
		return
	}

	// Determine log level based on depth
	logFn := log.Info
	msg := "Reorg audit: completed"
	
	if event.Depth >= ra.config.CriticalDepth {
		logFn = log.Error
		msg = "Reorg audit: CRITICAL DEEP REORG"
	} else if event.Depth >= ra.config.WarnDepth {
		logFn = log.Warn
		msg = "Reorg audit: deep reorg detected"
	}

	if !event.Success {
		logFn = log.Error
		msg = "Reorg audit: FAILED"
	}

	// Build log fields
	fields := []interface{}{
		"depth", event.Depth,
		"old_chain_len", event.OldChainLen,
		"new_chain_len", event.NewChainLen,
		"deleted_txs", event.DeletedTxs,
		"added_txs", event.AddedTxs,
		"duration", event.Duration,
	}

	if event.CommonBlock != nil {
		fields = append(fields, "common_block", formatBlockInfo(event.CommonBlock))
	}

	if ra.config.ValidateStateRoot {
		fields = append(fields, "state_root_ok", event.StateRootOK)
		if !event.StateRootOK {
			fields = append(fields,
				"old_root", event.OldStateRoot.Hex(),
				"new_root", event.NewStateRoot.Hex(),
			)
		}
	}

	if event.Error != nil {
		fields = append(fields, "error", event.Error)
	}

	logFn(msg, fields...)

	// Detailed logging if enabled
	if ra.config.DetailedLogs && event.Success && event.OldHead != nil && event.NewHead != nil {
		oldNum := event.OldHead.Number64()
		newNum := event.NewHead.Number64()
		var oldNumVal, newNumVal uint64
		if oldNum != nil {
			oldNumVal = oldNum.Uint64()
		}
		if newNum != nil {
			newNumVal = newNum.Uint64()
		}
		log.Debug("Reorg audit: detailed",
			"old_head_hash", event.OldHead.Hash().Hex(),
			"old_head_num", oldNumVal,
			"new_head_hash", event.NewHead.Hash().Hex(),
			"new_head_num", newNumVal,
			"old_state_root", event.OldStateRoot.Hex(),
			"new_state_root", event.NewStateRoot.Hex(),
		)
	}
}

// Stats returns reorg statistics.
func (ra *ReorgAudit) Stats() ReorgStats {
	stats := ReorgStats{
		TotalReorgs:  ra.totalReorgs.Load(),
		MaxDepthSeen: ra.maxDepthSeen.Load(),
	}
	
	lastTime := ra.lastReorgTime.Load()
	if lastTime > 0 {
		stats.LastReorgTime = time.Unix(lastTime, 0)
	}

	// Collect depth distribution
	stats.DepthDistribution = make(map[int]uint64)
	ra.reorgsByDepth.Range(func(key, value interface{}) bool {
		depth := key.(int)
		count := value.(*atomic.Uint64).Load()
		stats.DepthDistribution[depth] = count
		return true
	})

	return stats
}

// ReorgStats holds reorg statistics.
type ReorgStats struct {
	TotalReorgs       uint64
	MaxDepthSeen      uint64
	LastReorgTime     time.Time
	DepthDistribution map[int]uint64
}

// LogStats logs the current statistics.
func (ra *ReorgAudit) LogStats() {
	stats := ra.Stats()
	
	log.Info("Reorg audit: statistics",
		"total_reorgs", stats.TotalReorgs,
		"max_depth_seen", stats.MaxDepthSeen,
		"last_reorg_time", stats.LastReorgTime,
	)

	// Log depth distribution
	for depth, count := range stats.DepthDistribution {
		log.Debug("Reorg audit: depth distribution",
			"depth", depth,
			"count", count,
		)
	}
}

// formatBlockInfo formats block info for logging.
func formatBlockInfo(blk block.IBlock) string {
	if blk == nil {
		return "nil"
	}
	num := blk.Number64()
	if num == nil {
		return blk.Hash().Hex()[:10] + "...@?"
	}
	return blk.Hash().Hex()[:10] + "..." + "@" + num.String()
}

// =============================================================================
// Global Reorg Audit Instance
// =============================================================================

var globalReorgAudit = NewReorgAudit(nil)

// GetReorgAudit returns the global reorg audit instance.
func GetReorgAudit() *ReorgAudit {
	return globalReorgAudit
}

// SetReorgAuditConfig sets the global reorg audit configuration.
func SetReorgAuditConfig(config *ReorgAuditConfig) {
	globalReorgAudit = NewReorgAudit(config)
}

