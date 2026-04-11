// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Wire types for the eth/69 devp2p protocol. forkID carries the four-
// byte fork checksum and next-fork block, statusPacket mirrors the
// geth/Hive status handshake (protocol version, network id, genesis,
// forkID, latest block) and hashOrNumber selects between block keys.

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
