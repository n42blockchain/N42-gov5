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
// Server-side snapshot RPC handlers. snapshotInfoRPCHandler advertises
// available snapshot heights from the local snapshot.ListSnapshots
// registry so peers can pick a compatible checkpoint to download.

package sync

import (
	"context"
	"encoding/binary"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// snapshotInfoRPCHandler returns available snapshot heights to the requesting peer.
func (s *Service) snapshotInfoRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.SnapshotInfoHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	_, ok := msg.(*sync_pb.GetSnapshotInfoRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetSnapshotInfoRequest")
	}

	resp := &sync_pb.SnapshotInfoResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		snapshots, err := snapshot.ListSnapshots(tx)
		if err != nil {
			return err
		}
		for _, m := range snapshots {
			resp.Snapshots = append(resp.Snapshots, &sync_pb.SnapshotInfoEntry{
				BlockNumber:  m.BlockNumber,
				AccountCount: m.AccountCount,
				StorageCount: m.StorageCount,
				CodeCount:    m.CodeCount,
			})
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

// snapshotAccountRangeRPCHandler serves account state at a specific snapshot height.
// Data is collected into a batch, compressed with zstd, and sent as a single
// CompressedBatchResponse to minimize bandwidth.
func (s *Service) snapshotAccountRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.SnapshotAccountRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetSnapshotAccountRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetSnapshotAccountRangeRequest")
	}

	if req.SnapshotBlock == 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "snapshot block must be > 0", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.Start) > 0 && len(req.Start) != 20 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "start key must be 20 bytes", stream)
		return p2ptypes.ErrInvalidRequest
	}

	var startAddr types.Address
	if len(req.Start) == 20 {
		startAddr = types.BytesToAddress(req.Start)
	}

	resp := &sync_pb.CompressedBatchResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		// Validate snapshot exists.
		m, err := snapshot.LoadSnapshot(tx, req.SnapshotBlock)
		if err != nil {
			return err
		}
		if m == nil {
			return errors.New("snapshot not found")
		}

		var batch []snapshot.BatchEntry
		var totalBytes int
		var nextAddr *types.Address

		err = state.WalkAsOfAccounts(tx, startAddr, req.SnapshotBlock, func(k []byte, v []byte) (bool, error) {
			if len(k) != types.AddressLength {
				return true, nil
			}
			if len(batch) >= maxSnapAccountEntries || totalBytes >= maxSnapResponseBytes {
				addr := types.BytesToAddress(k)
				nextAddr = &addr
				return false, nil
			}
			key := make([]byte, len(k))
			copy(key, k)
			val := make([]byte, len(v))
			copy(val, v)
			batch = append(batch, snapshot.BatchEntry{Key: key, Value: val})
			totalBytes += len(k) + len(v)
			return true, nil
		})
		if err != nil {
			return err
		}

		compressed, err := snapshot.EncodeBatch(batch)
		if err != nil {
			return err
		}

		resp.EntryCount = uint32(len(batch))
		resp.Data = compressed
		if nextAddr != nil {
			resp.NextStart = nextAddr[:]
		} else {
			resp.Completed = true
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

// snapshotStorageRangeRPCHandler serves storage state at a specific snapshot height.
// Data is batch-compressed with zstd.
func (s *Service) snapshotStorageRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.SnapshotStorageRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetSnapshotStorageRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetSnapshotStorageRangeRequest")
	}

	if req.SnapshotBlock == 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "snapshot block must be > 0", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.Account) != 20 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "account address must be 20 bytes", stream)
		return p2ptypes.ErrInvalidRequest
	}

	addr := types.BytesToAddress(req.Account)
	var startLoc types.Hash
	if len(req.Start) == 32 {
		startLoc = types.BytesToHash(req.Start)
	}

	resp := &sync_pb.CompressedBatchResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		m, err := snapshot.LoadSnapshot(tx, req.SnapshotBlock)
		if err != nil {
			return err
		}
		if m == nil {
			return errors.New("snapshot not found")
		}

		var batch []snapshot.BatchEntry
		var totalBytes int
		var nextLoc *types.Hash

		err = state.WalkAsOfStorage(tx, addr, startLoc, req.SnapshotBlock, func(a, loc, v []byte) (bool, error) {
			if len(batch) >= maxSnapStorageEntries || totalBytes >= maxSnapResponseBytes {
				hash := types.BytesToHash(loc)
				nextLoc = &hash
				return false, nil
			}
			key := make([]byte, len(loc))
			copy(key, loc)
			val := make([]byte, len(v))
			copy(val, v)
			batch = append(batch, snapshot.BatchEntry{Key: key, Value: val})
			totalBytes += len(loc) + len(v)
			return true, nil
		})
		if err != nil {
			return err
		}

		compressed, err := snapshot.EncodeBatch(batch)
		if err != nil {
			return err
		}

		resp.EntryCount = uint32(len(batch))
		resp.Data = compressed
		if nextLoc != nil {
			resp.NextStart = nextLoc[:]
		} else {
			resp.Completed = true
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

// changeSetRangeRPCHandler serves account changesets in a block range for incremental sync.
// Changesets are batch-compressed with zstd. Each batch entry key = blockNumber(8) + address(20),
// value = old account data.
func (s *Service) changeSetRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.ChangeSetRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetChangeSetRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetChangeSetRangeRequest")
	}

	if req.FromBlock >= req.ToBlock {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "fromBlock must be < toBlock", stream)
		return p2ptypes.ErrInvalidRequest
	}

	resp := &sync_pb.CompressedBatchResponse{}

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		entries, err := snapshot.ReadAccountChangeSets(tx, req.FromBlock, req.ToBlock, 8192)
		if err != nil {
			return err
		}

		batch := make([]snapshot.BatchEntry, len(entries))
		for i, e := range entries {
			// key = blockNumber(8 bytes BE) + address
			key := make([]byte, 8+len(e.Address))
			binary.BigEndian.PutUint64(key[:8], e.BlockNumber)
			copy(key[8:], e.Address)
			batch[i] = snapshot.BatchEntry{Key: key, Value: e.Data}
		}

		compressed, err := snapshot.EncodeBatch(batch)
		if err != nil {
			return err
		}

		resp.EntryCount = uint32(len(entries))
		resp.Data = compressed
		resp.Completed = len(entries) < 8192
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
