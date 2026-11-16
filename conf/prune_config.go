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
// Historical data pruning configuration.
// Declares PruneMode ("archive", "full") and PruneConfig with
// BlockRetention window and PruneInterval cadence. The archive
// mode keeps full history; full mode trims changesets older than
// BlockRetention while respecting snapshot boundaries.

package conf

// PruneMode defines how aggressively historical data is pruned.
type PruneMode string

const (
	// PruneModeArchive keeps all historical data (default).
	PruneModeArchive PruneMode = "archive"
	// PruneModeFull keeps only recent history within the retention window.
	PruneModeFull PruneMode = "full"
)

// PruneConfig controls automatic pruning of historical blockchain data.
type PruneConfig struct {
	// Mode selects the pruning strategy: "archive" (default) or "full".
	Mode PruneMode `json:"mode" yaml:"mode"`

	// BlockRetention is the number of recent blocks whose changesets and
	// history are kept. Older data is pruned. Only used when Mode is "full".
	// Default: 90000 (~3 days at 3s block time).
	BlockRetention uint64 `json:"block_retention" yaml:"block_retention"`

	// PruneInterval is how often (in blocks) the pruner runs.
	// Default: 1000.
	PruneInterval uint64 `json:"prune_interval" yaml:"prune_interval"`

	// PruneBatchLimit is the maximum number of DB entries deleted per
	// pruning cycle. Smaller values reduce I/O spikes.
	// Default: 10000.
	PruneBatchLimit int `json:"prune_batch_limit" yaml:"prune_batch_limit"`

	// PruneReceipts controls whether old receipts and logs are pruned.
	// Default: false.
	PruneReceipts bool `json:"prune_receipts" yaml:"prune_receipts"`
}

const (
	DefaultBlockRetention = 90000
	DefaultPruneInterval  = 1000
	DefaultPruneBatchLimit = 10000
)

// IsEnabled returns true if pruning is active.
func (c *PruneConfig) IsEnabled() bool {
	return c.Mode == PruneModeFull
}

// QMDB unwind reach. The QMDB undo window (recent-height proofs and the
// HotStuff sibling-switch unwind) and the startup three-state realign both
// rewind PlainState through changesets (UnwindPlainStateBlock), so full-mode
// pruning must keep at least that many recent blocks of changesets.
const (
	// DefaultQMDBUndoWindow is the default and minimum undo window.
	DefaultQMDBUndoWindow = 256
	// MaxQMDBUndoWindow caps the undo window: a recent-height proof replays
	// up to that many undo records while holding the prover lock.
	MaxQMDBUndoWindow = 8192
	// QMDBRealignMaxDepth is the deepest startup realign (blocks).
	QMDBRealignMaxDepth = 512
	// PruneRetentionMargin is kept on top of the unwind reach so the pruner,
	// which runs between blocks, never races an unwind at the boundary.
	PruneRetentionMargin = 128
)

// EffectiveQMDBUndoWindow returns the undo window a configured value yields
// (0 or below the minimum = the minimum).
func EffectiveQMDBUndoWindow(n uint64) uint64 {
	if n < DefaultQMDBUndoWindow {
		return DefaultQMDBUndoWindow
	}
	return n
}

// MinPruneRetention is the smallest full-mode retention that keeps every
// changeset an unwind (sibling switch, realign) can still need.
func MinPruneRetention(undoWindow uint64) uint64 {
	reach := EffectiveQMDBUndoWindow(undoWindow)
	if reach < QMDBRealignMaxDepth {
		reach = QMDBRealignMaxDepth
	}
	return reach + PruneRetentionMargin
}
