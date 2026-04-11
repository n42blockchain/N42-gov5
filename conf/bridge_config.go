// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cross-chain bridge configuration.
// Holds BridgeCfg for the N42 -> Ethereum state-root publisher
// (batch size, poll interval, SP1 prover endpoint, verifier and
// bridge contract addresses) plus the reverse ETH light-client
// path and optional Hyperlane multi-chain mailbox / ISM wiring.

package conf

import "time"

// BridgeCfg holds the cross-chain bridge configuration.
type BridgeCfg struct {
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Publisher (N42→ETH state root anchoring via ZK proof)
	PublisherBatchSize    uint64        `json:"publisherBatchSize" yaml:"publisherBatchSize"`
	PublisherPollInterval time.Duration `json:"publisherPollInterval" yaml:"publisherPollInterval"`
	PublisherStartBlock   uint64        `json:"publisherStartBlock" yaml:"publisherStartBlock"`

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
