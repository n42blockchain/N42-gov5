// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eDonkey/ed2k hash bridge configuration.
// Ed2kCfg enables ed2k link generation and content verification
// with MaxFileSize and ChunkSize (default 9728000 bytes, the
// canonical ed2k block). DefaultEd2kCfg starts the bridge disabled
// with a 1 GiB size cap.

package conf

// Ed2kCfg controls the eDonkey/ed2k hash bridge for content verification.
type Ed2kCfg struct {
	Enabled      bool  `json:"enabled" yaml:"enabled"`
	MaxFileSize  int64 `json:"max_file_size" yaml:"max_file_size"`
	ChunkSize    int   `json:"chunk_size" yaml:"chunk_size"`
	EnableLinks  bool  `json:"enable_links" yaml:"enable_links"`
}

func DefaultEd2kCfg() Ed2kCfg {
	return Ed2kCfg{
		Enabled:     false,
		MaxFileSize: 1 << 30,     // 1GB
		ChunkSize:   9728000,     // standard ed2k chunk size (9500 KiB)
		EnableLinks: true,
	}
}
