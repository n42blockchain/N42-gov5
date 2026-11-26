// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"errors"
	"fmt"
	"io"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/rlp"
)

// RangeSource is what the bodies_by_range/2 server reads.
type RangeSource interface {
	// CommittedHead is the highest number the server may serve.
	CommittedHead() uint64
	// EarliestBody is the lowest number whose body is held.
	EarliestBody() uint64
	BlockAt(n uint64) block.IBlock
	HeaderAt(n uint64) *block.Header
	// Digest is the 4 context bytes (genesis hash prefix).
	Digest() [4]byte
}

// EncodeBlockPayload RLP-encodes a block for a chunk and enforces the
// block cap.
func EncodeBlockPayload(b block.IBlock) ([]byte, error) {
	data, err := rlp.EncodeToBytes(b)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) > encoder.MaxBlockChunkSize {
		return nil, fmt.Errorf("block #%d encodes to %d bytes, over the %d byte cap", b.Number64().Uint64(), len(data), encoder.MaxBlockChunkSize)
	}
	return data, nil
}

// ServeRangeV2 writes the reply to req through write (false = the stream
// is gone) and returns the END it sent: committed canonical blocks (or
// headers) contiguous and parent-linked from req.Start, stopping before a
// payload that would exceed the byte budget (the first is always sent), at
// the committed head (CANONICAL_END) or at a missing or unlinked block or a
// body below the earliest held (NOT_AVAILABLE).
func ServeRangeV2(src RangeSource, req syncwire.RangeRequest, write func([]byte) bool) syncwire.RangeEnd {
	end := syncwire.RangeEnd{Next: req.Start}
	budget := req.Budget()
	head := src.CommittedHead()
	digest := src.Digest()
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
			h := src.HeaderAt(n)
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
			if n < src.EarliestBody() {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			b := src.BlockAt(n)
			if b == nil {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			enc, err := EncodeBlockPayload(b)
			if err != nil {
				end.Reason = syncwire.ReasonNotAvailable
				break
			}
			payload, parent, hash = enc, b.ParentHash(), b.Hash()
		}
		if n > req.Start && parent != prev {
			end.Reason = syncwire.ReasonNotAvailable // a canonical hole
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

// RangeV2Result is what a bodies_by_range/2 client read.
type RangeV2Result struct {
	Blocks  []*block.Block  // full bodies (HEADERS_ONLY unset)
	Headers []*block.Header // HEADERS_ONLY
	End     *syncwire.RangeEnd
}

// ErrRangeFault marks a provable fault of a bodies_by_range/2 server (§3.5).
var ErrRangeFault = errors.New("bodies-by-range/2: provable fault")

// DecodeBlock decodes a block chunk payload. internal/sync replaces it
// with its decoder (legacy protobuf fallback on old chains).
var DecodeBlock = func(payload []byte) (*block.Block, error) {
	b := new(block.Block)
	if err := rlp.DecodeBytes(payload, b); err != nil {
		return nil, err
	}
	return b, nil
}

// ReadRangeV2 reads one reply to req. A missing END is a soft fault (no
// error, End nil); an END that contradicts the chunks, a non-contiguous or
// unlinked chunk or more chunks than requested is ErrRangeFault. An error
// chunk after a prefix keeps the prefix; before any block it is returned as
// *syncwire.CodeError. beforeChunk, when set, runs before each read (read
// deadlines).
func ReadRangeV2(r io.Reader, req syncwire.RangeRequest, beforeChunk func()) (*RangeV2Result, error) {
	res := &RangeV2Result{}
	var chunks, bytes uint64
	var prev [32]byte
	max := encoder.MaxBlockChunkSize
	if req.HeadersOnly() {
		max = syncwire.MaxHeaderBytes
	}
	fault := func(format string, a ...interface{}) (*RangeV2Result, error) {
		return res, fmt.Errorf("%w: %s", ErrRangeFault, fmt.Sprintf(format, a...))
	}
	for {
		if beforeChunk != nil {
			beforeChunk()
		}
		c, err := syncwire.ReadChunk(r, true, max)
		if err != nil {
			if chunks > 0 || errors.Is(err, io.EOF) {
				return res, nil // missing END: keep what arrived
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
				if err := rlp.DecodeBytes(c.Payload, hdr); err != nil || hdr.Number == nil {
					return fault("header %d: %v", want, err)
				}
				number, hash, parent = hdr.Number.Uint64(), hdr.Hash(), hdr.ParentHash
				res.Headers = append(res.Headers, hdr)
			} else {
				blk, err := DecodeBlock(c.Payload)
				if err != nil {
					return fault("block %d: %v", want, err)
				}
				number, hash, parent = blk.Number64().Uint64(), blk.Hash(), blk.ParentHash()
				res.Blocks = append(res.Blocks, blk)
			}
			if number != want || (chunks > 0 && parent != prev) {
				return fault("chunk %d is block %d (parent-linked %v)", want, number, chunks == 0 || parent == prev)
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
			return nil, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
		}
	}
}
