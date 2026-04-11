// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package devp2p

import (
	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type forkID struct {
	Hash [4]byte
	Next uint64
}

// statusPacket matches the eth/69 status wire layout expected by Hive/geth.
type statusPacket struct {
	ProtocolVersion uint32
	NetworkID       uint64
	Genesis         types.Hash
	ForkID          forkID
	EarliestBlock   uint64
	LatestBlock     uint64
	LatestBlockHash types.Hash
}

type hashOrNumber struct {
	Hash   types.Hash
	Number uint64
}

type getBlockHeadersPacket struct {
	RequestID uint64
	Origin    hashOrNumber
	Amount    uint64
	Skip      uint64
	Reverse   bool
}

type blockHeadersPacket struct {
	RequestID uint64
	Headers   []*n42block.Header
}

type getBlockBodiesPacket struct {
	RequestID uint64
	Hashes    []types.Hash
}

type blockBody struct {
	Transactions [][]byte
	Uncles       []*n42block.Header
}

type blockBodiesPacket struct {
	RequestID uint64
	Bodies    []blockBody
}
