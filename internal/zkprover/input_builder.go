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

package zkprover

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

// BuildGuestInput constructs the serialized GuestInput for the zkVM guest program.
func BuildGuestInput(
	chainConfig *params.ChainConfig,
	blk block.IBlock,
	parentHeader block.IHeader,
	bw *witness.BlockWitness,
) ([]byte, error) {
	blockNumber, err := requireBlockNumber(blk, "block number unavailable")
	if err != nil {
		return nil, err
	}
	headerProto := blk.Header().ToProtoMessage()
	headerBytes, err := proto.Marshal(headerProto)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal block header: %w", err)
	}

	parentProto := parentHeader.ToProtoMessage()
	parentBytes, err := proto.Marshal(parentProto)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal parent header: %w", err)
	}

	txs := blk.Transactions()
	txBytes := make([][]byte, len(txs))
	for i, tx := range txs {
		txProto := tx.ToProtoMessage()
		txBytes[i], err = proto.Marshal(txProto)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transaction %d: %w", i, err)
		}
	}

	chainID, err := guestChainID(chainConfig)
	if err != nil {
		return nil, err
	}

	witnessBytes, err := witness.EncodeBinaryWitness(bw)
	if err != nil {
		return nil, fmt.Errorf("failed to encode witness: %w", err)
	}

	blockNum := blockNumber.Uint64()
	blockTime := blk.Time()

	input := &GuestInput{
		ChainID:      chainID,
		BlockHeader:  headerBytes,
		ParentHeader: parentBytes,
		Transactions: txBytes,
		Witness:      witnessBytes,
		ForkConfig:   buildGuestForkConfig(chainConfig, blockNum, blockTime),
	}

	return EncodeGuestInput(input)
}

func buildGuestForkConfig(chainConfig *params.ChainConfig, blockNum, blockTime uint64) ForkConfig {
	if chainConfig == nil {
		return ForkConfig{}
	}
	return ForkConfig{
		IsHomestead:           chainConfig.IsHomestead(blockNum),
		IsEIP150:              chainConfig.IsTangerineWhistle(blockNum),
		IsEIP155:              chainConfig.IsSpuriousDragon(blockNum),
		IsEIP158:              chainConfig.IsSpuriousDragon(blockNum),
		IsByzantium:           chainConfig.IsByzantium(blockNum),
		IsConstantinople:      chainConfig.IsConstantinople(blockNum),
		IsPetersburg:          chainConfig.IsPetersburg(blockNum),
		IsIstanbul:            chainConfig.IsIstanbul(blockNum),
		IsBerlin:              chainConfig.IsBerlin(blockNum),
		IsLondon:              chainConfig.IsLondon(blockNum),
		IsShanghai:            chainConfig.IsShanghai(blockNum),
		IsCancun:              chainConfig.IsCancun(blockNum),
		IsBeijing:             chainConfig.IsBeijing(blockNum),
		IsPrague:              chainConfig.IsPrague(blockTime),
		IsPectra:              chainConfig.IsPectra(blockTime),
		IsOsaka:               chainConfig.IsOsaka(blockTime),
		IsFusaka:              chainConfig.IsFusaka(blockTime),
		IsNano:                chainConfig.IsNano(blockNum),
		IsMoran:               chainConfig.IsMoran(blockNum),
		IsEip1559FeeCollector: chainConfig.IsEip1559FeeCollector(blockNum),
		IsParlia:              chainConfig.UsesParliaRules(),
		IsAura:                chainConfig.UsesAuraRules(),
		IsPQPrecompiles:       chainConfig.IsPQPrecompiles(blockTime),
	}
}

func guestChainID(chainConfig *params.ChainConfig) (uint64, error) {
	if chainConfig == nil || chainConfig.ChainID == nil {
		return 0, fmt.Errorf("chain ID unavailable")
	}
	return chainConfig.ChainID.Uint64(), nil
}
