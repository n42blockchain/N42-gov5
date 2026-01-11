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

package eth69

import (
	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/utils"
)

// ConvertStatusToProtobuf converts an eth/69 StatusPacket to protobuf format.
func ConvertStatusToProtobuf(status *StatusPacket) *sync_pb.Status {
	if status == nil {
		return nil
	}

	return &sync_pb.Status{
		ProtocolVersion: status.ProtocolVersion,
		NetworkID:       status.NetworkID,
		GenesisHash:     utils.ConvertHashToH256(status.Genesis),
		CurrentHeight:   utils.ConvertUint256IntToH256(status.LatestBlock),
		EarliestBlock:   status.EarliestBlock,
		LatestBlock:     status.LatestBlock,
		LatestBlockHash: utils.ConvertHashToH256(status.LatestBlockHash),
		ForkID:          status.ForkID,
	}
}

// ConvertStatusFromProtobuf converts a protobuf Status message to eth/69 StatusPacket.
func ConvertStatusFromProtobuf(pbStatus *sync_pb.Status) *StatusPacket {
	if pbStatus == nil {
		return nil
	}

	return &StatusPacket{
		ProtocolVersion: pbStatus.ProtocolVersion,
		NetworkID:       pbStatus.NetworkID,
		Genesis:         utils.ConvertH256ToHash(pbStatus.GenesisHash),
		ForkID:          pbStatus.ForkID,
		EarliestBlock:   pbStatus.EarliestBlock,
		LatestBlock:     pbStatus.LatestBlock,
		LatestBlockHash: utils.ConvertH256ToHash(pbStatus.LatestBlockHash),
	}
}

// ConvertBlockRangeUpdateToProtobuf converts a BlockRangeUpdatePacket to protobuf.
func ConvertBlockRangeUpdateToProtobuf(update *BlockRangeUpdatePacket) *sync_pb.BlockRangeUpdate {
	if update == nil {
		return nil
	}

	return &sync_pb.BlockRangeUpdate{
		EarliestBlock:   update.EarliestBlock,
		LatestBlock:     update.LatestBlock,
		LatestBlockHash: utils.ConvertHashToH256(update.LatestBlockHash),
	}
}

// ConvertBlockRangeUpdateFromProtobuf converts a protobuf BlockRangeUpdate to native format.
func ConvertBlockRangeUpdateFromProtobuf(pbUpdate *sync_pb.BlockRangeUpdate) *BlockRangeUpdatePacket {
	if pbUpdate == nil {
		return nil
	}

	return &BlockRangeUpdatePacket{
		EarliestBlock:   pbUpdate.EarliestBlock,
		LatestBlock:     pbUpdate.LatestBlock,
		LatestBlockHash: utils.ConvertH256ToHash(pbUpdate.LatestBlockHash),
	}
}

// ValidateStatusCompatibility checks if a received status is compatible with eth/69.
// It ensures backward compatibility with eth/68 while supporting eth/69 features.
func ValidateStatusCompatibility(pbStatus *sync_pb.Status) error {
	status := ConvertStatusFromProtobuf(pbStatus)
	if status == nil {
		return ErrInvalidStatus
	}

	// Check protocol version support
	if !IsProtocolVersionSupported(uint(status.ProtocolVersion)) {
		return ErrUnsupportedProtocolVersion
	}

	// For eth/69, validate block range
	if status.ProtocolVersion >= ETH69 {
		if err := status.ValidateBlockRange(); err != nil {
			return err
		}
	}

	return nil
}

// Example integration with sync service:
//
// import "github.com/n42blockchain/N42/internal/network/eth69"
//
// // In sync service initialization:
// eth69Handler := eth69.NewHandler(
//     service.chain,
//     service.networkID,
//     0,  // earliestBlock (0 for archive node)
//     service,  // implements PeerSender
// )
//
// // Store handler in service
// service.eth69Handler = eth69Handler
//
// // In status message handler:
// func (s *Service) handleStatus(ctx context.Context, msg *sync_pb.Status, peerID peer.ID) error {
//     // Validate and convert
//     status := eth69.ConvertStatusFromProtobuf(msg)
//     if err := s.eth69Handler.HandleStatusMessage(peerID, status); err != nil {
//         return err
//     }
//
//     // Continue with existing logic...
//     return nil
// }
//
// // In block import handler:
// func (s *Service) onBlockImported(block *types.Block) {
//     // Notify eth/69 handler
//     s.eth69Handler.OnNewBlock(block)
//
//     // Continue with existing logic...
// }
//
// // In peer disconnect handler:
// func (s *Service) onPeerDisconnect(peerID peer.ID) {
//     // Cleanup eth/69 state
//     s.eth69Handler.OnPeerDisconnect(peerID)
//
//     // Continue with existing logic...
// }
