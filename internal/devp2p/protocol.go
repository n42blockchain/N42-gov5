// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Wire types for the eth/69 devp2p protocol. forkID carries the four-
// byte fork checksum and next-fork block, statusPacket mirrors the
// geth/Hive status handshake (protocol version, network id, genesis,
// forkID, latest block) and hashOrNumber selects between block keys.

package devp2p

import (
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/rlp"

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

// hashOrNumber is a union: exactly one of Hash / Number is on the
// wire, never both. Default reflect-based RLP would emit a two-field
// list and any non-N42 peer would Disconnect; the custom EncodeRLP /
// DecodeRLP below match the eth/68-69 spec exactly.
type hashOrNumber struct {
	Hash   types.Hash
	Number uint64
}

func (h *hashOrNumber) EncodeRLP(w io.Writer) error {
	if h.Hash == (types.Hash{}) {
		return rlp.Encode(w, h.Number)
	}
	if h.Number != 0 {
		return fmt.Errorf("both origin hash (%x) and number (%d) provided", h.Hash, h.Number)
	}
	return rlp.Encode(w, h.Hash)
}

func (h *hashOrNumber) DecodeRLP(s *rlp.Stream) error {
	origin, err := s.Raw()
	if err != nil {
		return err
	}
	switch {
	case len(origin) == 33:
		return rlp.DecodeBytes(origin, &h.Hash)
	case len(origin) <= 9:
		return rlp.DecodeBytes(origin, &h.Number)
	default:
		return fmt.Errorf("invalid origin size %d", len(origin))
	}
}

// getBlockHeadersQuery is the INNER wire payload — the eth/68-69 spec
// wraps this inside `[reqID, [...]]`. See getBlockHeadersPacket below.
type getBlockHeadersQuery struct {
	Origin  hashOrNumber
	Amount  uint64
	Skip    uint64
	Reverse bool
}

// getBlockHeadersPacket matches the wire form `[reqID, [origin, amount,
// skip, reverse]]`. The pointer-to-inner-struct embedding is how geth/
// reth/erigon all encode this — encoding a 5-field flat struct here
// produces wire bytes the rest of the network rejects with
// DiscProtocolError ("breach of protocol").
type getBlockHeadersPacket struct {
	RequestID uint64
	*getBlockHeadersQuery
}

// blockHeadersPacket carries headers as RAW RLP bytes per entry. The
// alternative — decoding into `[]*n42block.Header` via reflect — fails
// against real mainnet wire bytes because N42's Header struct has post-
// London fork fields without `rlp:"optional"` tags (touching those
// would alter Header's hash). Consumers decode each entry with the
// existing ethel.DecodeUncleHeader, which knows the per-fork layout.
type blockHeadersPacket struct {
	RequestID uint64
	Headers   []rlp.RawValue
}

type getBlockBodiesPacket struct {
	RequestID uint64
	Hashes    []types.Hash
}

// BlockBody is the eth/69 wire-form body. Exported so external
// downloaders (internal/ethel/eldevp2p) can decode it.
//
// All three slots use `rlp.RawValue` rather than `[]byte` because the
// transaction list mixes RLP shapes: legacy txs are RLP lists
// (`[nonce, gasPrice, ...]`), EIP-2718 typed txs are RLP strings
// (`type || rlp(payload)`). A `[]byte` element forces RLP to expect
// only strings — on a legacy tx (block 25M+ still has many) it errors
// with "expected input string or byte for []uint8". `rlp.RawValue` is
// the only safe element type for a wire list with mixed inner shapes.
type BlockBody struct {
	Transactions []rlp.RawValue
	Uncles       []rlp.RawValue
	// Withdrawals optionally encoded — geth peers serving post-Shanghai
	// blocks include this slot. Pre-Shanghai blocks omit it (rlp:"optional").
	Withdrawals []rlp.RawValue `rlp:"optional"`
}

type blockBodiesPacket struct {
	RequestID uint64
	Bodies    []BlockBody
}

// getBlockAccessListsPacket is the eth/71-style GetBlockAccessLists (0x12)
// request: the block hashes whose full EIP-7928 BAL is wanted (out of band; the
// header only carries the BAL hash).
type getBlockAccessListsPacket struct {
	RequestID uint64
	Hashes    []types.Hash
}

// blockAccessListsPacket is the BlockAccessLists (0x13) response: the raw BAL RLP
// per requested hash, in request order, each carried as a byte string (an empty
// string = "not held", encoded as 0x80 — avoids the malformed-RawValue decode
// break geth hit in 3006c4411).
type blockAccessListsPacket struct {
	RequestID uint64
	BALs      [][]byte
}

// blockReceiptsPacket is the eth/68-69 Receipts (0x10) response. Each receipt is
// kept as rlp.RawValue (mixed shapes: legacy receipts are RLP lists, typed ones
// are RLP strings wrapping type||payload) — a []byte element would reject legacy.
type blockReceiptsPacket struct {
	RequestID uint64
	Receipts  [][]rlp.RawValue // per requested block → its receipts
}

// blockRangeUpdatePacket is eth/69 msg 0x11. Sent right after Status to
// (re-)announce the local block range. Required by the eth/69 spec — peers
// treat its absence as a protocol violation and silently EOF after a
// few hundred ms.
type blockRangeUpdatePacket struct {
	EarliestBlock   uint64
	LatestBlock     uint64
	LatestBlockHash types.Hash
}
