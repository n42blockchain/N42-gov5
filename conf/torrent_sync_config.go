// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// OtterSync (BitTorrent EraE chain-sync) configuration.
// TorrentSyncCfg sets SegmentSize (default 8192 blocks per EraE
// file), WebSeeds HTTP fallback list, ManifestURLs for torrent
// manifests and VerifyBlocks to hash-chain-check imported segments.
// DefaultTorrentSyncCfg starts disabled with verification on.

package conf

// TorrentSyncCfg controls OtterSync — BitTorrent-based chain sync via EraE segments.
type TorrentSyncCfg struct {
	Enabled      bool     `json:"enabled" yaml:"enabled"`
	SegmentSize  uint64   `json:"segment_size" yaml:"segment_size"`       // blocks per EraE file (default 8192)
	WebSeeds     []string `json:"web_seeds" yaml:"web_seeds"`             // HTTP fallback URLs
	ManifestURLs []string `json:"manifest_urls" yaml:"manifest_urls"`     // manifest fetch URLs
	VerifyBlocks bool     `json:"verify_blocks" yaml:"verify_blocks"`     // verify block hash chain
}

// DefaultTorrentSyncCfg returns sensible defaults for OtterSync.
func DefaultTorrentSyncCfg() TorrentSyncCfg {
	return TorrentSyncCfg{
		Enabled:      false,
		SegmentSize:  8192,
		WebSeeds:     nil,
		ManifestURLs: nil,
		VerifyBlocks: true,
	}
}
