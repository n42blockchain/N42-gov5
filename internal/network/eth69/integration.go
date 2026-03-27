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
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/utils"
)

// ConvertStatusToProtobuf converts an eth/69 StatusPacket to protobuf format.
// Note: The current sync_pb.Status only has GenesisHash and CurrentHeight fields.
// eth/69 specific fields (EarliestBlock, LatestBlock, etc.) are not included in the protobuf.
func ConvertStatusToProtobuf(status *StatusPacket) *sync_pb.Status {
	if status == nil {
		return nil
	}

	return &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(status.Genesis),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(status.LatestBlock)),
	}
}

// ConvertStatusFromProtobuf converts a protobuf Status message to eth/69 StatusPacket.
// Note: Fields not in sync_pb.Status (ProtocolVersion, NetworkID, etc.) will be set to defaults.
func ConvertStatusFromProtobuf(pbStatus *sync_pb.Status) *StatusPacket {
	if pbStatus == nil {
		return nil
	}

	// Convert CurrentHeight (H256) back to uint64
	var latestBlock uint64
	if pbStatus.CurrentHeight != nil {
		latestBlock = utils.ConvertH256ToUint256Int(pbStatus.CurrentHeight).Uint64()
	}

	return &StatusPacket{
		ProtocolVersion: ETH69, // Default to ETH69
		NetworkID:       0,     // Unknown from protobuf
		Genesis:         utils.ConvertH256ToHash(pbStatus.GenesisHash),
		ForkID:          nil,
		EarliestBlock:   0, // Unknown from protobuf
		LatestBlock:     latestBlock,
		LatestBlockHash: utils.ConvertH256ToHash(pbStatus.CurrentHeight), // Use CurrentHeight as a proxy
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

