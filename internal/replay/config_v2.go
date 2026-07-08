// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ConfigV2 struct tuning the replay-v2 engine. Holds source and target
// DB paths, block range, BatchSize, commitment tree flags (EnableJMT,
// EnableLtHash, TreeType), gap-filling thresholds, EraE export
// options, MPT verification toggle and structured log destinations.

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
	ChainName   string // chain name for GenesisByChainName (genesis alloc + consensus + state scheme); empty = mainnet_v2
	FromBlock   uint64
	ToBlock     uint64
	BatchSize   int

	EnableJMT    bool
	EnableLtHash bool
	DisableGC    bool
	TreeType     string // "jmt" or "bmt"

	// QMDBUndoWindow (qmdb tree only): per-block undo records are kept for the
	// last N blocks so eth_getProof can serve membership proofs at recent
	// heights (the QMDB root is history-dependent — see lib/qmdb/undo.go).
	// 0 disables recording.
	QMDBUndoWindow int

	// QMDBHistory (qmdb tree only): journal the FULL-history layer (per-slot
	// death stamps, per-key versions, per-block cursor, top band) so
	// ProofAtHeight can serve membership proofs at ANY height. Forces archival
	// entry-row retention. See lib/qmdb/history.go.
	QMDBHistory bool

	FillGaps     bool
	GapPeriod    uint64
	GapTolerance uint64
	GapMaxBlocks uint64 // cap on synthetic empty blocks per gap (0 = unlimited); guards OOM on a huge gap (startup/outage)
	VirtualTd    bool   // skip all-zero TD rows; ReadTd synthesizes 0 for known headers (marker in DatabaseInfo)

	ExportEraE      bool
	EraESegmentSize uint64
	SnapshotAtEnd   bool

	SkipAddresses map[types.Address]bool

	VerifyMPT bool // per-block: rebuild MPT from PlainState and verify root

	// AutoTopup (diagnostic): when a tx fails with "insufficient funds", credit
	// the sender the exact deficit (want-have) and retry, so the tx executes
	// instead of being skipped (no nonce cascade). The cumulative credit per
	// sender is the MINIMUM genesis balance that address needs for the chain to
	// replay cleanly under this fork schedule. Written to TopupDumpFile.
	AutoTopup     bool
	TopupDumpFile string

	LogFile     string // structured log output file (empty = stderr only)
	StatsFile   string // stats JSON output file (updated every batch)
	LeafJournal string // leaf change journal file (empty = disabled)
	ProgressFn  func(current, total uint64, bps float64)

	// BLS re-seal simulation (mobile-voter consensus). When BLSReseal is true,
	// each block's consensus proof is a CommitteeSize-member BLS aggregate QC
	// sampled from a PoolSize mobile-voter pool that ramps from one committee at
	// genesis up to PoolSize over RampBlocks blocks. The QC is written to the
	// ConsensusEvidence MDBX table and committed into the header via
	// ParentBeaconRoot (Blake3 of the parent evidence), which is also fed to the
	// EVM through the EIP-4788 beacon-roots contract.
	BLSReseal        bool
	BLSSeed          [32]byte
	BLSPoolSize      int
	BLSCommitteeSize int
	BLSRampBlocks    uint64
}

// DefaultConfigV2 returns a ConfigV2 with sensible defaults.
func DefaultConfigV2() ConfigV2 {
	return ConfigV2{
		BatchSize:       10000,
		EnableJMT:       true,
		EnableLtHash:    true,
		DisableGC:       true,
		TreeType:        "mpt",
		QMDBUndoWindow:  64,
		FillGaps:        true,
		GapPeriod:       8,
		GapTolerance:    15,
		GapMaxBlocks:    10000, // bound a single gap's fill so a multi-day outage/startup gap cannot OOM one batch
		EraESegmentSize: 8192,
		SnapshotAtEnd:   true,
		SkipAddresses:   DefaultSkipAddresses,
	}
}
