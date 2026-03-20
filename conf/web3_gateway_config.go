// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// Web3GatewayCfg controls the web3:// protocol gateway (ERC-6860).
// When enabled, serves on-chain content via HTTP by translating
// web3:// URLs into EVM calls.
type Web3GatewayCfg struct {
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	Host         string `json:"host" yaml:"host"`
	Port         int    `json:"port" yaml:"port"`
	CacheSeconds int    `json:"cache_seconds" yaml:"cache_seconds"`
}

func DefaultWeb3GatewayCfg() Web3GatewayCfg {
	return Web3GatewayCfg{
		Enabled:      false,
		Host:         "127.0.0.1",
		Port:         8080,
		CacheSeconds: 12,
	}
}
