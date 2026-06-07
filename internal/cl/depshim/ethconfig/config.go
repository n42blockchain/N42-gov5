// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package ethconfig is a depshim for erigon's node/ethconfig — it provides only
// the BlocksFreezing struct that the Caplin snapshot layer's constructors take.
// N42 does not use erigon's snapshot-distribution machinery; the Caplin snapshot
// readers run in DB-fallback mode (see internal/cl/depshim/snapshotsync), so the
// fields here are carried for signature fidelity when porting Caplin consumers,
// not consumed by N42's snapshot reading.

package ethconfig

// BlocksFreezing mirrors erigon's ethconfig.BlocksFreezing field set so ported
// Caplin code that constructs it (e.g. NewCaplinSnapshots / NewCaplinStateSnapshots)
// compiles unchanged. N42's shim snapshot layer ignores these values.
type BlocksFreezing struct {
	KeepBlocks        bool // produce new snapshots of blocks but don't remove blocks from DB
	ProduceE2         bool // produce new block files
	ProduceE3         bool // produce new state files
	NoDownloader      bool // possible to use snapshots without calling Downloader
	P2PManifest       bool // discover snapshot manifest from P2P peers
	DisableDownloadE3 bool // disable download state snapshots
	DownloaderAddr    string
	ChainName         string
	// ManifestReady is closed when P2P manifest discovery completes; nil otherwise.
	ManifestReady <-chan struct{}
}
