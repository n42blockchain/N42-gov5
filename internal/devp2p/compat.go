// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compatibility helpers that map N42 chain configuration to the Ethereum
// devp2p handshake. networkID resolves the numeric network id from a
// ChainConfig, and newForkID computes the eth/69 forkID checksum chain
// from block- and time-based hardfork schedules (internal/forkid, shared
// with eth_config).

package devp2p

import (
	n42types "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/forkid"
	n42params "github.com/n42blockchain/N42/params"
)

func networkID(cfg *n42params.ChainConfig) uint64 {
	if cfg == nil {
		return 0
	}
	if id := n42params.NetworkIDByChainName(cfg.ChainName); id != 0 {
		return id
	}
	if cfg.ChainID != nil {
		return cfg.ChainID.Uint64()
	}
	return 0
}

func newForkID(cfg *n42params.ChainConfig, genesisHash n42types.Hash, genesisTime, head, time uint64) forkID {
	id := forkid.NewID(cfg, genesisHash, genesisTime, head, time)
	return forkID{Hash: id.Hash, Next: id.Next}
}

func checksumToBytes(hash uint32) [4]byte {
	return forkid.ChecksumToBytes(hash)
}
