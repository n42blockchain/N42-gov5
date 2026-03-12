// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// This file implements the P2P witness protocol for requesting and serving
// block witnesses over the N42 P2P network.

package sync

import (
	"context"
	"fmt"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/modules/state/witness"
)

const (
	// GetBlockWitnessTopicV1 is the P2P topic for requesting block witnesses.
	GetBlockWitnessTopicV1 = "/n42/req/get_block_witness/1"

	// maxWitnessResponseSize is the maximum size of a witness response in bytes (16 MB).
	// Block witnesses for complex blocks with many state accesses can be large.
	maxWitnessResponseSize = 16 * 1024 * 1024
)

// GetBlockWitnessRequest is the P2P request for a block witness.
type GetBlockWitnessRequest struct {
	BlockHash types.Hash
}

// GetBlockWitnessResponse is the P2P response containing a block witness.
type GetBlockWitnessResponse struct {
	// Witness contains the serialized BlockWitness data (JSON encoded).
	// Empty when Found is false.
	Witness []byte
	// Found indicates whether the witness was available.
	Found bool
}

// witnessRPCHandler handles incoming GetBlockWitness P2P requests.
// It looks up the block witness for the requested block hash and returns
// it to the requesting peer. If the witness is not available (e.g., because
// JMT commitment is not enabled or the block was not locally produced),
// an error response is sent.
func (s *Service) witnessRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.WitnessHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*GetBlockWitnessRequest)
	if !ok {
		return errors.New("message is not type *GetBlockWitnessRequest")
	}

	emptyHash := types.Hash{}
	if req.BlockHash == emptyHash {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "block_hash must not be empty", stream)
		return p2ptypes.ErrInvalidRequest
	}

	// Look up the block to verify it exists.
	blk, err := s.cfg.chain.GetBlockByHash(req.BlockHash)
	if err != nil || blk == nil {
		s.writeErrorResponseToStream(responseCodeInvalidRequest,
			fmt.Sprintf("block not found: %x", req.BlockHash), stream)
		return p2ptypes.ErrInvalidRequest
	}

	// TODO(light-client): Look up the cached witness for this block.
	// In production, witnesses would be generated during block production
	// and cached in an LRU cache keyed by block hash. The witness cache
	// would be injected into the sync service config.
	//
	// The lookup flow would be:
	//   1. Check witness LRU cache for the block hash
	//   2. If found, encode the witness and write it to the stream
	//   3. If not found, return a server error
	//
	// Example (when witness cache is wired):
	//   w, found := s.witnessCache.Get(req.BlockHash)
	//   if !found {
	//       s.writeErrorResponseToStream(responseCodeServerError, "witness not cached", stream)
	//       return nil
	//   }
	//   data, err := w.Encode()
	//   if err != nil { ... }
	//   resp := &GetBlockWitnessResponse{Witness: data, Found: true}
	//   return writeSuccessResponse(stream, s.cfg.p2p, resp)

	s.writeErrorResponseToStream(responseCodeServerError,
		"block witness not available: JMT commitment witness cache not configured", stream)

	return nil
}

// DecodeWitnessResponse decodes a GetBlockWitnessResponse into a BlockWitness.
// This is a convenience function for the requesting peer.
func DecodeWitnessResponse(resp *GetBlockWitnessResponse) (*witness.BlockWitness, error) {
	if !resp.Found || len(resp.Witness) == 0 {
		return nil, errors.New("witness not available in response")
	}
	return witness.DecodeBlockWitness(resp.Witness)
}
