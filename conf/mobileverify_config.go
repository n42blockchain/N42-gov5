// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// MobileVerifyCfg configures the mobile attestation pipeline
// (docs/mobile-attestation-design.md). Disabled by default; when
// enabled, the node produces StreamPackets for blocks it seals,
// distributes them over gossip, and caches the fleet's packets in a
// rolling window for serving to mobile verifiers.
type MobileVerifyCfg struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// PacketWindow is how many recent blocks' packets to retain (design §4).
	PacketWindow uint64 `json:"packet_window" yaml:"packet_window"`
}

// DefaultMobileVerifyCfg returns the default (disabled) configuration.
func DefaultMobileVerifyCfg() MobileVerifyCfg {
	return MobileVerifyCfg{
		PacketWindow: 256,
	}
}
