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
// InputBuilder: assembles the witness blob (block header, receipts,
// MPT / JMT proofs, transaction senders) that the ZK guest program
// needs to re-execute a block deterministically. Pulls inputs from
// the local chain state and serialises them into the shared guest
// type layout.

package zkprover

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
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
	// Headers and transactions travel to the guest in the compact storage
	// codecs, the same ones the database is written in. Both sides are ours, and
	// the guest decodes through Header.Unmarshal / Transaction.Unmarshal, which
	// dispatch on the 0xFF marker -- so an input serialized before this change
	// still decodes. Dropping protobuf also takes its runtime out of the guest,
	// which is compiled to RISC-V and pays for every byte of it.
	headerBytes := marshalHeaderForGuest(blk.Header())
	parentBytes := marshalHeaderForGuest(parentHeader)

	txs := blk.Transactions()
	txBytes := make([][]byte, len(txs))
	for i, tx := range txs {
		txBytes[i], err = marshalTxForGuest(tx)
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

// marshalHeaderForGuest serializes a header for the guest program in the
// compact storage codec.
func marshalHeaderForGuest(h block.IHeader) []byte {
	if hdr, ok := h.(*block.Header); ok {
		return hdr.MarshalCompact()
	}
	// IHeader is satisfied only by *block.Header in this tree; the interface
	// exists for test doubles. Fall back rather than panicking on one.
	if m, ok := h.(interface{ MarshalCompact() []byte }); ok {
		return m.MarshalCompact()
	}
	return nil
}

// marshalTxForGuest serializes a transaction for the guest program, preferring
// the compact storage codec and falling back to protobuf for the transaction
// types it does not cover. Transaction.Unmarshal dispatches between them.
func marshalTxForGuest(tx *transaction.Transaction) ([]byte, error) {
	if enc := tx.MarshalCompactStorage(); enc != nil {
		return enc, nil
	}
	return tx.Marshal()
}
