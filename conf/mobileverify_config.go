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
	// HTTPAddr is the phone-facing listen address (design §3, §5b/c);
	// empty disables the HTTP surface (packet gossip/cache still run).
	HTTPAddr string `json:"http_addr" yaml:"http_addr"`
	// CollectWindowSec is the receipt collection span per block (§5c).
	CollectWindowSec int `json:"collect_window_sec" yaml:"collect_window_sec"`
	// CertBlocks is how many recent blocks' certificates to retain (§6).
	CertBlocks int `json:"cert_blocks" yaml:"cert_blocks"`
	// TorrentEnabled seeds cached packets on the BitTorrent bridge
	// (design §5b target form); requires TorrentDistCfg to be enabled
	// too — the mobile pipeline reuses that client.
	TorrentEnabled bool `json:"torrent_enabled" yaml:"torrent_enabled"`
	// RegisterPoWBits, when > 0, demands a registration proof-of-work of
	// that many leading zero bits at the phone-facing register endpoint
	// (design §7 item 2 — Sybil resistance without staking). 20 bits ≈ one
	// million hashes ≈ sub-second on a modern phone. 0 disables (default).
	RegisterPoWBits int `json:"register_pow_bits" yaml:"register_pow_bits"`
}

// DefaultMobileVerifyCfg returns the default (disabled) configuration.
func DefaultMobileVerifyCfg() MobileVerifyCfg {
	return MobileVerifyCfg{
		PacketWindow:     256,
		CollectWindowSec: 45,
		CertBlocks:       512,
	}
}
