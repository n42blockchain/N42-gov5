// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package shuffling

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state/raw"

	"github.com/n42blockchain/N42/internal/cl/utils"
)

func ComputeProposerIndex(b *raw.BeaconState, indices []uint64, seed [32]byte) (uint64, error) {
	if len(indices) == 0 {
		return 0, nil
	}
	if b.Version() >= clparams.ElectraVersion {
		return computeProposerIndexElectra(b, indices, seed)
	}

	// before electra case
	maxRandomByte := uint64(1<<8 - 1)
	i := uint64(0)
	total := uint64(len(indices))
	input := make([]byte, 40)
	preInputs := ComputeShuffledIndexPreInputs(b.BeaconConfig(), seed)
	for {
		shuffled, err := ComputeShuffledIndex(b.BeaconConfig(), i%total, total, seed, preInputs, utils.Sha256)
		if err != nil {
			return 0, err
		}
		candidateIndex := indices[shuffled]
		if candidateIndex >= uint64(b.ValidatorLength()) {
			return 0, fmt.Errorf("candidate index out of range: %d for validator set of length: %d", candidateIndex, b.ValidatorLength())
		}
		copy(input, seed[:])
		binary.LittleEndian.PutUint64(input[32:], i/32)
		randomByte := uint64(utils.Sha256(input)[i%32])
		validator, err := b.ValidatorForValidatorIndex(int(candidateIndex))
		if err != nil {
			return 0, err
		}
		if validator.EffectiveBalance()*maxRandomByte >= b.BeaconConfig().MaxEffectiveBalanceForVersion(b.Version())*randomByte {
			return candidateIndex, nil
		}
		i += 1
	}
}

func computeProposerIndexElectra(b *raw.BeaconState, indices []uint64, seed [32]byte) (uint64, error) {
	maxRandomValue := uint64(1<<16 - 1)
	i := uint64(0)
	total := uint64(len(indices))
	input := make([]byte, 40)
	preInputs := ComputeShuffledIndexPreInputs(b.BeaconConfig(), seed)
	for {
		shuffled, err := ComputeShuffledIndex(b.BeaconConfig(), i%total, total, seed, preInputs, utils.Sha256)
		if err != nil {
			return 0, err
		}
		candidateIndex := indices[shuffled]
		// [Modified in Electra]
		// random_bytes = hash(seed + uint_to_bytes(i // 16))
		// offset = i % 16 * 2
		// random_value = bytes_to_uint64(random_bytes[offset:offset + 2])
		copy(input, seed[:])
		binary.LittleEndian.PutUint64(input[32:], i/16)
		randomBytes := utils.Sha256(input)
		offset := (i % 16) * 2
		randomValue := binary.LittleEndian.Uint16(randomBytes[offset : offset+2])

		validator, err := b.ValidatorForValidatorIndex(int(candidateIndex))
		if err != nil {
			return 0, err
		}
		if validator.EffectiveBalance()*maxRandomValue >= b.BeaconConfig().MaxEffectiveBalanceForVersion(b.Version())*uint64(randomValue) {
			return candidateIndex, nil
		}
		i += 1
	}
}

func ComputeProposerIndices(b *raw.BeaconState, epoch uint64, seed [32]byte, indices []uint64) ([]uint64, error) {
	startSlot := epoch * b.BeaconConfig().SlotsPerEpoch
	proposerIndices := make([]uint64, b.BeaconConfig().SlotsPerEpoch)

	// Generate seed for each slot
	input := make([]byte, 40)
	copy(input, seed[:])
	for i := uint64(0); i < b.BeaconConfig().SlotsPerEpoch; i++ {
		// Hash seed + slot to get per-slot seed
		binary.LittleEndian.PutUint64(input[32:], startSlot+i)
		slotSeed := utils.Sha256(input)

		// Compute proposer index for this slot
		proposerIndex, err := ComputeProposerIndex(b, indices, slotSeed)
		if err != nil {
			return nil, err
		}
		proposerIndices[i] = proposerIndex
	}

	return proposerIndices, nil
}
