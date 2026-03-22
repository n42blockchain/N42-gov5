// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// IngestCfg configures the binary TCP ingest server for high-throughput
// transaction injection during stress testing.
type IngestCfg struct {
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	Addr       string `json:"addr" yaml:"addr"`
	SoftTarget int    `json:"soft_target" yaml:"soft_target"`
	HardCap    int    `json:"hard_cap" yaml:"hard_cap"`
}

// DefaultIngestCfg returns the default ingest configuration.
func DefaultIngestCfg() IngestCfg {
	return IngestCfg{
		Enabled:    false,
		Addr:       ":9100",
		SoftTarget: 50000,
		HardCap:    100000,
	}
}
