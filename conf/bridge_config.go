// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import "time"

// BridgeCfg holds the cross-chain bridge configuration.
// Mirrors internal/bridge.Config but lives in conf/ to avoid import cycles.
type BridgeCfg struct {
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Relayer (N42→ETH ZK proof submission)
	RelayerBatchSize    uint64        `json:"relayerBatchSize" yaml:"relayerBatchSize"`
	RelayerPollInterval time.Duration `json:"relayerPollInterval" yaml:"relayerPollInterval"`
	RelayerStartBlock   uint64        `json:"relayerStartBlock" yaml:"relayerStartBlock"`

	// DA Publisher (periodic state root anchoring)
	DAPublishInterval uint64        `json:"daPublishInterval" yaml:"daPublishInterval"`
	DAPollInterval    time.Duration `json:"daPollInterval" yaml:"daPollInterval"`

	// Ethereum target chain
	EthRPCEndpoint  string `json:"ethRpcEndpoint" yaml:"ethRpcEndpoint"`
	VerifierAddress string `json:"verifierAddress" yaml:"verifierAddress"`
	BridgeAddress   string `json:"bridgeAddress" yaml:"bridgeAddress"`

	// SP1 prover
	SP1Endpoint    string `json:"sp1Endpoint" yaml:"sp1Endpoint"`
	SP1GuestBinary string `json:"sp1GuestBinary" yaml:"sp1GuestBinary"`

	// ETH Light Client (reverse bridge)
	EthLightClientEnabled bool   `json:"ethLightClientEnabled" yaml:"ethLightClientEnabled"`
	EthBeaconEndpoint     string `json:"ethBeaconEndpoint" yaml:"ethBeaconEndpoint"`

	// Hyperlane (multi-chain)
	HyperlaneEnabled    bool   `json:"hyperlaneEnabled" yaml:"hyperlaneEnabled"`
	HyperlaneMailbox    string `json:"hyperlaneMailbox" yaml:"hyperlaneMailbox"`
	HyperlaneISMAddress string `json:"hyperlaneIsmAddress" yaml:"hyperlaneIsmAddress"`
	HyperlaneN42Domain  uint32 `json:"hyperlaneN42Domain" yaml:"hyperlaneN42Domain"`
}
