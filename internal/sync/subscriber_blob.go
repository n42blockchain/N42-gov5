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

package sync

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// blobSidecarSubscriber handles incoming blob sidecar messages from gossip.
// It validates the sidecar and stores it in the database.
func (s *Service) blobSidecarSubscriber(ctx context.Context, msg proto.Message) error {
	pbSidecar, ok := msg.(*types_pb.BlobSidecar)
	if !ok {
		log.Error("Blob sidecar subscriber received wrong message type")
		return errWrongMessage
	}

	sc, err := gossipMsgToBlobSidecar(pbSidecar)
	if err != nil {
		log.Warn("Failed to convert gossip blob sidecar", "err", err)
		return err
	}

	if err := sc.Validate(); err != nil {
		log.Warn("Received invalid blob sidecar", "err", err)
		return err
	}

	log.Info("Subscriber received blob sidecar",
		"index", sc.Index,
		"block_number", sc.BlockNumber,
		"block_hash", sc.BlockHash,
	)

	// Store the blob sidecar in the database.
	if err := s.cfg.chain.DB().Update(ctx, func(tx kv.RwTx) error {
		// Read existing sidecars for this block.
		existing, err := rawdb.ReadBlobSidecars(tx, sc.BlockNumber, sc.BlockHash)
		if err != nil {
			return err
		}

		// Check for duplicates.
		for _, e := range existing {
			if e.Index == sc.Index {
				log.Debug("Blob sidecar already stored, skipping",
					"index", sc.Index,
					"block_number", sc.BlockNumber,
				)
				return nil
			}
		}

		// Append and store.
		existing = append(existing, sc)
		return rawdb.WriteBlobSidecars(tx, sc.BlockNumber, sc.BlockHash, existing)
	}); err != nil {
		log.Error("Failed to store blob sidecar", "err", err)
		return err
	}

	return nil
}

// gossipMsgToBlobSidecar converts a types_pb.BlobSidecar to block.BlobSidecar.
func gossipMsgToBlobSidecar(pb *types_pb.BlobSidecar) (*block.BlobSidecar, error) {
	sc := &block.BlobSidecar{
		Index:       pb.Index,
		BlockNumber: pb.BlockNumber,
	}

	if len(pb.Blob) == block.BlobSize {
		copy(sc.Blob[:], pb.Blob)
	}
	if len(pb.KzgCommitment) == block.KZGCommitmentSize {
		copy(sc.KZGCommitment[:], pb.KzgCommitment)
	}
	if len(pb.KzgProof) == block.KZGProofSize {
		copy(sc.KZGProof[:], pb.KzgProof)
	}
	if len(pb.BlockHash) == 32 {
		sc.BlockHash = types.BytesToHash(pb.BlockHash)
	}
	if len(pb.TxHash) == 32 {
		sc.TxHash = types.BytesToHash(pb.TxHash)
	}

	return sc, nil
}
