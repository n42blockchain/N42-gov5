// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// TorrentDistCfg controls the distributed BitTorrent storage bridge.
// This is separate from the snapshot downloader's torrent config —
// it handles CAS↔torrent bridging for on-chain content distribution.
type TorrentDistCfg struct {
	Enabled       bool   `json:"enabled" yaml:"enabled"`
	DataDir       string `json:"data_dir" yaml:"data_dir"`
	ListenAddr    string `json:"listen_addr" yaml:"listen_addr"`
	MaxSeedBytes  int64  `json:"max_seed_bytes" yaml:"max_seed_bytes"`
	UploadRateKB  int    `json:"upload_rate_kb" yaml:"upload_rate_kb"`
	DownloadRateKB int   `json:"download_rate_kb" yaml:"download_rate_kb"`
	MaxActiveTorrents int `json:"max_active_torrents" yaml:"max_active_torrents"`
	EnableDHT     bool   `json:"enable_dht" yaml:"enable_dht"`
	EnablePEX     bool   `json:"enable_pex" yaml:"enable_pex"`
	SeedOnStore   bool   `json:"seed_on_store" yaml:"seed_on_store"`
	PieceSize     int    `json:"piece_size" yaml:"piece_size"`
}

func DefaultTorrentDistCfg() TorrentDistCfg {
	return TorrentDistCfg{
		Enabled:           false,
		DataDir:           "torrent_data",
		ListenAddr:        ":42069",
		MaxSeedBytes:      1 << 30, // 1GB
		UploadRateKB:      0,       // unlimited
		DownloadRateKB:    0,       // unlimited
		MaxActiveTorrents: 100,
		EnableDHT:         true,
		EnablePEX:         true,
		SeedOnStore:       true,
		PieceSize:         256 * 1024, // 256KB pieces for CAS content
	}
}
