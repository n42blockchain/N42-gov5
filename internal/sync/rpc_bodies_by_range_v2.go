// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// /rpc/bodies_by_range/2/ssz_snappy (N42 sync standard §5.1): committed-only
// ranges with a requester byte budget, HEADERS_ONLY, and an END trailer that
// says where and why the reply stopped. /1 stays served for v1 peers; the
// catch-up client prefers /2 when a peer lists it in identify.

package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
)

// rangeSource is what the /2 server reads.
type rangeSource interface {
	// committedHead is the highest number the server may serve.
	committedHead() uint64
	// earliestBody is the lowest number whose body is held.
	earliestBody() uint64
	blockAt(n uint64) block.IBlock
	headerAt(n uint64) *block.Header
	digest() [4]byte
}

type chainRangeSource struct{ s *Service }

func (c chainRangeSource) committedHead() uint64 {
	if ceil, ok := c.s.committedServeCeiling(); ok {
		return ceil
	}
	return currentBlockNumberOrZero(c.s.cfg.chain)
}

func (c chainRangeSource) earliestBody() uint64 {
	if c.s.cfg.earliestBlock != nil {
		return c.s.cfg.earliestBlock()
	}
	return c.s.cfg.chain.EarliestBlock()
}

func (c chainRangeSource) blockAt(n uint64) block.IBlock {
	b, err := c.s.cfg.chain.GetBlockByNumber(uint256.NewInt(n))
	if err != nil {
		return nil
	}
	return b
}

func (c chainRangeSource) headerAt(n uint64) *block.Header {
	h, _ := c.s.cfg.chain.GetHeaderByNumber(uint256.NewInt(n)).(*block.Header)
	return h
}

func (c chainRangeSource) digest() (d [4]byte) {
	copy(d[:], c.s.cfg.chain.GenesisBlock().Hash().Bytes()[:4])
	return d
}

// serveRangeV2 writes the reply to req through write (false = the stream is
// gone) and returns the END it sent.
func serveRangeV2(src rangeSource, req syncwire.RangeRequest, write func([]byte) bool) syncwire.RangeEnd {
	end := syncwire.RangeEnd{Next: req.Start}
	budget := req.Budget()
	head := src.committedHead()
	digest := src.digest()
	last := req.Start + req.Count - 1
	var prev [32]byte
	for n := req.Start; n <= last; n++ {
		if n > head {
			end.Reason = syncwire.ReasonCanonicalEnd
			break
		}
		var payload []byte
		var parent, hash [32]byte
		if req.HeadersOnly() {
			h := src.headerAt(n)
			if h == nil {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			enc, err := rlp.EncodeToBytes(h)
			if err != nil || len(enc) > syncwire.MaxHeaderBytes {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			payload, parent, hash = enc, h.ParentHash, h.Hash()
		} else {
			if n < src.earliestBody() {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			b := src.blockAt(n)
			if b == nil {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			enc, err := encodeBlockChunkPayload(b)
			if err != nil {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			payload, parent, hash = enc, b.ParentHash(), b.Hash()
		}
		// Contiguous and parent-linked, or stop (a canonical hole).
		if n > req.Start && parent != prev {
			end.Reason = syncwire.ReasonNotAvailable
			break
		}
		if n > req.Start && end.ServedBytes+uint64(len(payload)) > budget {
			end.Reason = syncwire.ReasonByteBudget
			break
		}
		if !write(syncwire.AppendChunk(nil, syncwire.CodeSuccess, &digest, payload)) {
			return end
		}
		end.ServedBytes += uint64(len(payload))
		end.Next = n + 1
		prev = hash
	}
	if end.Next == req.Start+req.Count {
		end.Reason = syncwire.ReasonComplete
	}
	write(syncwire.AppendChunk(nil, syncwire.CodeEnd, nil, end.Encode()))
	return end
}

// bodiesByRangeV2Handler is the stream handler of /2.
func (s *Service) bodiesByRangeV2Handler(_ context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	req, err := syncwire.DecodeRangeRequest(raw)
	if err != nil {
		// Malformed: refused (§5.0 code 1). Not scored here: the request
		// never reached the database.
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	end := serveRangeV2(chainRangeSource{s}, req, func(b []byte) bool { return writeV11(stream, b) })
	log.Debug("bodies-by-range/2: served", "peer", shortPeer(pid), "start", req.Start, "count", req.Count,
		"headersOnly", req.HeadersOnly(), "next", end.Next, "bytes", end.ServedBytes, "reason", end.Reason)
}

// RangeV2Result is what a /2 client read.
type RangeV2Result struct {
	Blocks  []*block.Block  // full bodies (HEADERS_ONLY unset)
	Headers []*block.Header // HEADERS_ONLY
	End     *syncwire.RangeEnd
}

// errRangeV2Fault marks a provable fault of a /2 server (§3.5).
var errRangeV2Fault = errors.New("bodies-by-range/2: provable fault")

// SendBodiesByRangeV2 requests req from pid over /2 and returns what was
// served. A missing END is a soft fault (no error, End nil); an END that
// contradicts the chunks, a non-contiguous chunk or more chunks than
// requested is a provable fault (errRangeV2Fault wrapped in
// ErrInvalidFetchedData). An error chunk after a prefix keeps the prefix.
func SendBodiesByRangeV2(ctx context.Context, h host.Host, pid peer.ID, req syncwire.RangeRequest) (*RangeV2Result, error) {
	stream, err := openV11(ctx, h, pid, syncwire.ProtocolBodiesByRangeV2, req.Encode())
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	return readRangeV2(stream, req)
}

func readRangeV2(r io.Reader, req syncwire.RangeRequest) (*RangeV2Result, error) {
	res := &RangeV2Result{}
	var chunks, bytes uint64
	var prev [32]byte
	max := encoder.MaxBlockChunkSize
	if req.HeadersOnly() {
		max = syncwire.MaxHeaderBytes
	}
	fault := func(format string, a ...interface{}) (*RangeV2Result, error) {
		return res, fmt.Errorf("%w: %w: %s", ErrInvalidFetchedData, errRangeV2Fault, fmt.Sprintf(format, a...))
	}
	for {
		if sr, ok := r.(network.Stream); ok {
			_ = sr.SetReadDeadline(time.Now().Add(respTimeout))
		}
		c, err := syncwire.ReadChunk(r, true, max)
		if err != nil {
			if chunks > 0 || errors.Is(err, io.EOF) {
				// Missing END: soft fault, keep what arrived.
				return res, nil
			}
			return nil, err
		}
		switch c.Code {
		case syncwire.CodeSuccess:
			if chunks >= req.Count {
				return fault("more chunks than the count %d", req.Count)
			}
			want := req.Start + chunks
			var number uint64
			var hash, parent [32]byte
			if req.HeadersOnly() {
				hdr := new(block.Header)
				if err := rlp.DecodeBytes(c.Payload, hdr); err != nil {
					return fault("header %d: %v", want, err)
				}
				number, hash, parent = hdr.Number.Uint64(), hdr.Hash(), hdr.ParentHash
				res.Headers = append(res.Headers, hdr)
			} else {
				blk, err := decodeChunkedBlock(c.Payload)
				if err != nil {
					return fault("block %d: %v", want, err)
				}
				number, hash, parent = blk.Number64().Uint64(), blk.Hash(), blk.ParentHash()
				res.Blocks = append(res.Blocks, blk)
			}
			if number != want || (chunks > 0 && parent != prev) {
				return fault("chunk %d is block %d, parent-linked %v", want, number, chunks == 0 || parent == prev)
			}
			prev = hash
			chunks++
			bytes += uint64(len(c.Payload))
		case syncwire.CodeEnd:
			e, err := syncwire.DecodeRangeEnd(c.Payload)
			if err != nil {
				return fault("END: %v", err)
			}
			if err := syncwire.CheckRangeEnd(req, e, chunks, bytes); err != nil {
				return fault("%v", err)
			}
			res.End = &e
			return res, nil
		default:
			if chunks > 0 {
				return res, nil // an error after a prefix: keep the prefix
			}
			return nil, v11CodeErr(c)
		}
	}
}

// rangeV2Blocks adapts a /2 request to the v1 catch-up callers: it returns
// the blocks of [start, start+count) that pid served.
func rangeV2Blocks(ctx context.Context, h host.Host, pid peer.ID, start, count uint64) ([]*block.Block, error) {
	res, err := SendBodiesByRangeV2(ctx, h, pid, syncwire.RangeRequest{Start: start, Count: count})
	if err != nil {
		return nil, err
	}
	if res.End != nil && res.End.Reason == syncwire.ReasonRateLimited {
		log.Debug("bodies-by-range/2: server rate limited", "peer", shortPeer(pid))
	}
	return res.Blocks, nil
}
