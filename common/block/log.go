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
// EVM Log record produced by LOG0..LOG4 opcodes. Carries the
// emitting Address, topic list, raw Data and the context fields
// (BlockNumber, TxHash, TxIndex, BlockHash, Index, Removed) needed
// for eth_getLogs filtering and reorg handling. ToProtoMessage /
// FromProtoMessage map to proto/types_pb.Log for gossip and RPC
// via the H160 / H256 converters in common/utils.

package block

import (
	"fmt"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
)

type Log struct {
	Address types.Address `json:"address" gencodec:"required"`
	Topics  []types.Hash  `json:"topics" gencodec:"required"`
	Data    []byte        `json:"data" gencodec:"required"`

	BlockNumber *uint256.Int `json:"blockNumber"`
	TxHash      types.Hash   `json:"transactionHash" gencodec:"required"`
	TxIndex     uint         `json:"transactionIndex" gencodec:"required"`
	BlockHash   types.Hash   `json:"blockHash"`
	Index       uint         `json:"logIndex" gencodec:"required"`
	Removed     bool         `json:"removed"`
}

func (l *Log) ToProtoMessage() proto.Message {
	return &types_pb.Log{
		Address:     utils.ConvertAddressToH160(l.Address),
		Topics:      utils.ConvertHashesToH256(l.Topics),
		Data:        l.Data,
		BlockNumber: utils.ConvertUint256IntToH256(l.BlockNumber),
		TxHash:      utils.ConvertHashToH256(l.TxHash),
		TxIndex:     uint64(l.TxIndex),
		BlockHash:   utils.ConvertHashToH256(l.BlockHash),
		Index:       uint64(l.Index),
		Removed:     l.Removed,
	}
}

func (l *Log) FromProtoMessage(message proto.Message) error {
	pLog, ok := message.(*types_pb.Log)
	if !ok {
		return fmt.Errorf("type conversion failure")
	}

	l.Address = utils.ConvertH160toAddress(pLog.Address)
	l.Topics = utils.H256sToHashes(pLog.Topics)
	l.Data = pLog.Data
	l.BlockNumber = utils.ConvertH256ToUint256Int(pLog.BlockNumber)
	l.TxHash = utils.ConvertH256ToHash(pLog.TxHash)
	l.TxIndex = uint(pLog.TxIndex)
	l.BlockHash = utils.ConvertH256ToHash(pLog.BlockHash)
	l.Index = uint(pLog.Index)
	l.Removed = pLog.Removed

	return nil
}

type Logs []*Log

// Marshal encodes the log list as protobuf. Retained to read back data written
// before the compact codec; MarshalCompact is what write paths use.
func (l *Logs) Marshal() ([]byte, error) {
	pbLogs := make([]*types_pb.Log, len(*l))
	for i, log := range *l {
		pbLogs[i] = log.ToProtoMessage().(*types_pb.Log)
	}
	return proto.Marshal(&types_pb.Logs{Logs: pbLogs})
}

func (l *Logs) Unmarshal(data []byte) error {
	// Compact storage format (0xFF marker — never a valid proto first byte,
	// since 0xFF decodes as field 31 / wire type 7 which is illegal).
	if IsCompactLogs(data) {
		return l.unmarshalCompact(data)
	}
	pb := new(types_pb.Logs)
	if err := proto.Unmarshal(data, pb); err != nil {
		return err
	}

	body := make([]*Log, len(pb.Logs))
	for i, p := range pb.Logs {
		body[i] = new(Log)
		if err := body[i].FromProtoMessage(p); err != nil {
			return err
		}
	}
	*l = body
	return nil
}
