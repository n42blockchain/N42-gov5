// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// ConfigV2 configures the replay-v2 engine.
type ConfigV2 struct {
	SourcePath  string
	TargetPath  string
	ChainConfig *params.ChainConfig
	FromBlock   uint64
	ToBlock     uint64
	BatchSize   int

	EnableJMT    bool
	EnableLtHash bool
	DisableGC    bool
	TreeType     string // "jmt" or "bmt"

	FillGaps     bool
	GapPeriod    uint64
	GapTolerance uint64

	ExportEraE      bool
	EraESegmentSize uint64
	SnapshotAtEnd   bool

	SkipAddresses map[types.Address]bool

	LogFile    string // structured log output file (empty = stderr only)
	StatsFile  string // stats JSON output file (updated every batch)
	ProgressFn func(current, total uint64, bps float64)
}

// DefaultConfigV2 returns a ConfigV2 with sensible defaults.
func DefaultConfigV2() ConfigV2 {
	return ConfigV2{
		BatchSize:       50000,
		EnableJMT:       true,
		EnableLtHash:    true,
		DisableGC:       true,
		TreeType:        "jmt",
		FillGaps:        true,
		GapPeriod:       8,
		GapTolerance:    15,
		EraESegmentSize: 8192,
		SnapshotAtEnd:   true,
		SkipAddresses:   DefaultSkipAddresses,
	}
}
