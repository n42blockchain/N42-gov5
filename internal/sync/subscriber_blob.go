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
// GossipSub subscriber for EIP-4844 blob sidecars. blobSidecarSubscriber
// decodes the sidecar from the same compact encoding rawdb stores it in,
// validates it via sc.Validate and persists it so block verification can pair
// each block with its committed blobs.

package sync

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// blobSidecarSubscriber handles incoming blob sidecar messages from gossip.
// It validates the sidecar and stores it in the database.
func (s *Service) blobSidecarSubscriber(ctx context.Context, data any) error {
	raw, ok := data.(*rawSSZBytes)
	if !ok {
		log.Error("Blob sidecar subscriber received wrong message type")
		return errWrongMessage
	}

	sc, err := decodeGossipBlobSidecar(raw.data)
	if err != nil {
		log.Warn("Failed to decode gossip blob sidecar", "err", err)
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

// decodeGossipBlobSidecar reads one sidecar from the wire.
//
// The wire form is rawdb's storage encoding, so a sidecar has a single
// serialized representation across gossip and the database. It used to be SSZ
// over a generated protobuf struct, whose conversion silently dropped any
// field whose length did not match -- a sidecar with a truncated blob or KZG
// commitment arrived as one with a zeroed blob or commitment, and only
// Validate stood between that and storage.
func decodeGossipBlobSidecar(data []byte) (*block.BlobSidecar, error) {
	sidecars, err := rawdb.DecodeBlobSidecars(data)
	if err != nil {
		return nil, err
	}
	if len(sidecars) != 1 {
		return nil, fmt.Errorf("blob sidecar gossip carries exactly one sidecar, got %d", len(sidecars))
	}
	return sidecars[0], nil
}
