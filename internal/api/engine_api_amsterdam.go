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

// Engine API methods introduced with Amsterdam: engine_newPayloadV5.
// Reference: https://github.com/ethereum/execution-apis/blob/main/src/engine/amsterdam.md

package api

import (
	"context"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// ExecutionPayloadAmsterdam is the Amsterdam execution payload, the spec's
// ExecutionPayloadV4: the Prague payload plus the RLP-encoded EIP-7928 block
// access list and the EIP-7843 slot number. (N42's ExecutionPayloadV4 is the
// Prague payload, hence the fork name here.)
type ExecutionPayloadAmsterdam struct {
	ExecutionPayloadV4
	BlockAccessList hexutil.Bytes   `json:"blockAccessList"`
	SlotNumber      *hexutil.Uint64 `json:"slotNumber"`
}

// amsterdamPayloadFields are the header inputs an Amsterdam payload adds.
type amsterdamPayloadFields struct {
	blockAccessList []byte
	slotNumber      uint64
}

// apply sets the block access list hash, keccak256 of the payload's encoded
// list, and the slot number on the block's header.
func (f *amsterdamPayloadFields) apply(blk block.IBlock) block.IBlock {
	header := blockHeader(blk)
	if header == nil {
		return blk
	}
	header = block.CopyHeader(header)
	balHash := crypto.Keccak256Hash(f.blockAccessList)
	slot := f.slotNumber
	header.BlockAccessListHash = &balHash
	header.SlotNumber = &slot
	return blk.WithSeal(header)
}

// NewPayloadV5 validates and imports an Amsterdam payload.
// engine_newPayloadV5
//
// Missing fields are invalid params; a block access list that is present but
// does not decode, or does not match the list built by executing the block,
// makes the payload INVALID.
func (e *EngineAPIv4) NewPayloadV5(
	ctx context.Context,
	payload *ExecutionPayloadAmsterdam,
	expectedBlobVersionedHashes []types.Hash,
	parentBeaconBlockRoot *types.Hash,
	executionRequests []hexutil.Bytes,
) (*PayloadStatusV1, error) {
	switch {
	case payload == nil:
		return nil, &engineInvalidParamsError{msg: "missing execution payload"}
	case payload.Withdrawals == nil:
		return nil, &engineInvalidParamsError{msg: "nil withdrawals post-shanghai"}
	case payload.BlobGasUsed == nil || payload.ExcessBlobGas == nil:
		return nil, &engineInvalidParamsError{msg: "missing blob gas fields"}
	case expectedBlobVersionedHashes == nil:
		return nil, &engineInvalidParamsError{msg: "nil versionedHashes post-cancun"}
	case parentBeaconBlockRoot == nil:
		return nil, &engineInvalidParamsError{msg: "nil beaconRoot post-cancun"}
	case executionRequests == nil:
		return nil, &engineInvalidParamsError{msg: "nil executionRequests post-prague"}
	case payload.SlotNumber == nil:
		return nil, &engineInvalidParamsError{msg: "nil slotNumber post-amsterdam"}
	case payload.BlockAccessList == nil:
		return nil, &engineInvalidParamsError{msg: "missing block access list post-amsterdam"}
	}
	if !engineForkIn(e.v1.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), engineForkAmsterdam) {
		return nil, &engineUnsupportedForkError{msg: "newPayloadV5 called for a timestamp outside Amsterdam"}
	}
	return e.newPayloadV4(&payload.ExecutionPayloadV4, expectedBlobVersionedHashes, parentBeaconBlockRoot, executionRequests, &amsterdamPayloadFields{
		blockAccessList: payload.BlockAccessList,
		slotNumber:      uint64(*payload.SlotNumber),
	})
}
