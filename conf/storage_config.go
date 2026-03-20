// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// StorageCfg controls the decentralized storage bridge (IPFS/Filecoin).
type StorageCfg struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	IPFSAPIAddr string `json:"ipfs_api_addr" yaml:"ipfs_api_addr"`
	MaxPinSize  int64  `json:"max_pin_size" yaml:"max_pin_size"`
	MaxGetSize  int64  `json:"max_get_size" yaml:"max_get_size"`
	PinTimeout  int    `json:"pin_timeout" yaml:"pin_timeout"`
	GetTimeout  int    `json:"get_timeout" yaml:"get_timeout"`
	GatewayURL  string `json:"gateway_url" yaml:"gateway_url"`
}

func DefaultStorageCfg() StorageCfg {
	return StorageCfg{
		Enabled:     false,
		IPFSAPIAddr: "localhost:5001",
		MaxPinSize:  1 << 20, // 1MB
		MaxGetSize:  1 << 20,
		PinTimeout:  60,
		GetTimeout:  30,
		GatewayURL:  "https://ipfs.io",
	}
}
