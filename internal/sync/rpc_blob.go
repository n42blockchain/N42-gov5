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
//
// Server-side RPC handlers for blob sidecar requests. Implements
// blobSidecarsByRangeRPCHandler and by-root delivery bounded by
// maxBlobRangeCount, maxBlobRootIdentifiers and maxBlobResponseSidecars
// to stream EIP-4844 sidecars to syncing peers.

package sync

import (
	"context"
	"fmt"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const (
	// maxBlobRangeCount is the maximum number of blocks to serve blob sidecars for in a range request.
	maxBlobRangeCount = 128
	// maxBlobRootIdentifiers is the maximum identifiers in a by-root request.
	maxBlobRootIdentifiers = 64
	// maxBlobResponseSidecars is the maximum total sidecars per response.
	maxBlobResponseSidecars = 768 // 128 blocks * 6 blobs
)

// blobSidecarsByRangeRPCHandler handles BlobSidecarsByRange requests.
// It reads blob sidecars for a range of blocks from the database.
func (s *Service) blobSidecarsByRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.BlobSidecarsByRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.BlobSidecarsByRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.BlobSidecarsByRangeRequest")
	}

	if req.Count == 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "count must be > 0", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if req.Count > maxBlobRangeCount {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, fmt.Sprintf("count %d exceeds max %d", req.Count, maxBlobRangeCount), stream)
		return p2ptypes.ErrInvalidRequest
	}

	resp := &sync_pb.BlobSidecarsResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		for i := uint64(0); i < req.Count; i++ {
			blockNum := req.StartBlock + i

			// Look up canonical hash for block number.
			blockHash, err := rawdb.ReadCanonicalHash(tx, blockNum)
			if err != nil {
				return err
			}
			if blockHash == (types.Hash{}) {
				// Block not found, stop iteration.
				break
			}

			sidecars, err := rawdb.ReadBlobSidecars(tx, blockNum, blockHash)
			if err != nil {
				return err
			}
			if sidecars == nil {
				continue // no blob sidecars for this block
			}

			for _, sc := range sidecars {
				resp.Sidecars = append(resp.Sidecars, blobSidecarToMsg(sc))
				if len(resp.Sidecars) >= maxBlobResponseSidecars {
					return nil // stop early if limit reached
				}
			}
		}
		return nil
	}); err != nil {
		s.writeErrorResponseToStream(responseCodeServerError, err.Error(), stream)
		return err
	}

	if err := s.writeSnapResponse(stream, resp); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

// blobSidecarsByRootRPCHandler handles BlobSidecarsByRoot requests.
// It looks up specific blob sidecars by block hash and index.
func (s *Service) blobSidecarsByRootRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.BlobSidecarsByRootHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.BlobSidecarsByRootRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.BlobSidecarsByRootRequest")
	}

	if len(req.Identifiers) == 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "no identifiers provided", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.Identifiers) > maxBlobRootIdentifiers {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "too many identifiers", stream)
		return p2ptypes.ErrInvalidRequest
	}

	for _, id := range req.Identifiers {
		if len(id.BlockRoot) != 32 {
			s.writeErrorResponseToStream(responseCodeInvalidRequest, "block root must be 32 bytes", stream)
			return p2ptypes.ErrInvalidRequest
		}
		if id.Index >= block.MaxBlobsPerBlock {
			s.writeErrorResponseToStream(responseCodeInvalidRequest, fmt.Sprintf("blob index %d exceeds max", id.Index), stream)
			return p2ptypes.ErrInvalidRequest
		}
	}

	resp := &sync_pb.BlobSidecarsResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		for _, id := range req.Identifiers {
			blockHash := types.BytesToHash(id.BlockRoot)

			// Look up block number from hash.
			blockNum := rawdb.ReadHeaderNumber(tx, blockHash)
			if blockNum == nil {
				continue // block not found, skip
			}

			sidecars, err := rawdb.ReadBlobSidecars(tx, *blockNum, blockHash)
			if err != nil {
				return err
			}
			if sidecars == nil {
				continue
			}

			// Find the sidecar with the matching index.
			for _, sc := range sidecars {
				if sc.Index == id.Index {
					resp.Sidecars = append(resp.Sidecars, blobSidecarToMsg(sc))
					break
				}
			}
		}
		return nil
	}); err != nil {
		s.writeErrorResponseToStream(responseCodeServerError, err.Error(), stream)
		return err
	}

	if err := s.writeSnapResponse(stream, resp); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

// blobSidecarToMsg converts a block.BlobSidecar to its P2P message representation.
func blobSidecarToMsg(sc *block.BlobSidecar) *sync_pb.BlobSidecarMsg {
	msg := &sync_pb.BlobSidecarMsg{
		Index:         sc.Index,
		Blob:          make([]byte, block.BlobSize),
		KZGCommitment: make([]byte, block.KZGCommitmentSize),
		KZGProof:      make([]byte, block.KZGProofSize),
		BlockNumber:   sc.BlockNumber,
		BlockHash:     sc.BlockHash.Bytes(),
		TxHash:        sc.TxHash.Bytes(),
	}
	copy(msg.Blob, sc.Blob[:])
	copy(msg.KZGCommitment, sc.KZGCommitment[:])
	copy(msg.KZGProof, sc.KZGProof[:])
	return msg
}

// msgToBlobSidecar converts a P2P BlobSidecarMsg to a block.BlobSidecar.
func msgToBlobSidecar(msg *sync_pb.BlobSidecarMsg) (*block.BlobSidecar, error) {
	if len(msg.Blob) != block.BlobSize {
		return nil, fmt.Errorf("invalid blob size: %d", len(msg.Blob))
	}
	if len(msg.KZGCommitment) != block.KZGCommitmentSize {
		return nil, fmt.Errorf("invalid KZG commitment size: %d", len(msg.KZGCommitment))
	}
	if len(msg.KZGProof) != block.KZGProofSize {
		return nil, fmt.Errorf("invalid KZG proof size: %d", len(msg.KZGProof))
	}
	if len(msg.BlockHash) != 32 {
		return nil, fmt.Errorf("invalid block hash size: %d", len(msg.BlockHash))
	}

	sc := &block.BlobSidecar{
		Index:       msg.Index,
		BlockNumber: msg.BlockNumber,
		BlockHash:   types.BytesToHash(msg.BlockHash),
		TxHash:      types.BytesToHash(msg.TxHash),
	}
	copy(sc.Blob[:], msg.Blob)
	copy(sc.KZGCommitment[:], msg.KZGCommitment)
	copy(sc.KZGProof[:], msg.KZGProof)

	return sc, nil
}
