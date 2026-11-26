// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// /rpc/bodies_by_range/2/ssz_snappy (N42 sync standard §5.1): committed-only
// ranges with a requester byte budget, HEADERS_ONLY, and an END trailer that
// says where and why the reply stopped. /1 stays served for v1 peers; the
// catch-up client prefers /2 when a peer lists it in identify. The protocol
// core is v11.ServeRangeV2 / v11.ReadRangeV2.

package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/log"
)

func init() {
	// Blocks on the v1.1 wire decode like v1 chunks (legacy protobuf
	// fallback only where AllowLegacyProtoBlocks enabled it).
	v11.DecodeBlock = decodeChunkedBlock
}

// chainRangeSource serves ranges from the node's chain.
type chainRangeSource struct{ s *Service }

func (c chainRangeSource) CommittedHead() uint64 {
	if ceil, ok := c.s.committedServeCeiling(); ok {
		return ceil
	}
	return currentBlockNumberOrZero(c.s.cfg.chain)
}

func (c chainRangeSource) EarliestBody() uint64 {
	if c.s.cfg.earliestBlock != nil {
		return c.s.cfg.earliestBlock()
	}
	return c.s.cfg.chain.EarliestBlock()
}

func (c chainRangeSource) BlockAt(n uint64) block.IBlock {
	b, err := c.s.cfg.chain.GetBlockByNumber(uint256.NewInt(n))
	if err != nil {
		return nil
	}
	return b
}

func (c chainRangeSource) HeaderAt(n uint64) *block.Header {
	h, _ := c.s.cfg.chain.GetHeaderByNumber(uint256.NewInt(n)).(*block.Header)
	return h
}

func (c chainRangeSource) Digest() (d [4]byte) {
	copy(d[:], c.s.cfg.chain.GenesisBlock().Hash().Bytes()[:4])
	return d
}

// bodiesByRangeV2Handler is the stream handler of /2.
func (s *Service) bodiesByRangeV2Handler(_ context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	req, err := syncwire.DecodeRangeRequest(raw)
	if err != nil {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	end := v11.ServeRangeV2(chainRangeSource{s}, req, func(b []byte) bool { return writeV11(stream, b) })
	log.Info("bodies-by-range/2: served", "peer", shortPeer(pid), "start", req.Start, "count", req.Count,
		"headersOnly", req.HeadersOnly(), "next", end.Next, "bytes", end.ServedBytes, "reason", end.Reason)
}

// SendBodiesByRangeV2 requests req from pid over /2. A provable fault is
// returned wrapped in ErrInvalidFetchedData so the catch-up scores it.
func SendBodiesByRangeV2(ctx context.Context, h host.Host, pid peer.ID, req syncwire.RangeRequest) (*v11.RangeV2Result, error) {
	stream, err := openV11(ctx, h, pid, syncwire.ProtocolBodiesByRangeV2, req.Encode())
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	res, err := v11.ReadRangeV2(stream, req, func() { _ = stream.SetReadDeadline(time.Now().Add(respTimeout)) })
	if err != nil && errors.Is(err, v11.ErrRangeFault) {
		return res, fmt.Errorf("%w: %w", ErrInvalidFetchedData, err)
	}
	return res, err
}

// rangeV2Blocks adapts a /2 request to the v1 catch-up callers: it returns
// the blocks of [start, start+count) that pid served.
func rangeV2Blocks(ctx context.Context, h host.Host, pid peer.ID, start, count uint64) ([]*block.Block, error) {
	res, err := SendBodiesByRangeV2(ctx, h, pid, syncwire.RangeRequest{Start: start, Count: count})
	if err != nil {
		return nil, err
	}
	return res.Blocks, nil
}
