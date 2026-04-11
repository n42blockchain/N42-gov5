// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Fork unit for the fork package.
// Exports helpers such as ComputeDomain, ComputeSigningRoot, and Domain.
// Fork digest and signing domain computation.

//go:build n42el

package fork

import (
	"errors"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/sszh"
)

var NO_GENESIS_TIME_ERR error = errors.New("genesis time is not set")
var NO_VALIDATOR_ROOT_HASH error = errors.New("genesis validators root is not set")

func ComputeDomain(
	domainType []byte,
	currentVersion [4]byte,
	genesisValidatorsRoot [32]byte,
) ([]byte, error) {
	var currentVersion32 common.Hash
	copy(currentVersion32[:], currentVersion[:])
	forkDataRoot := utils.Sha256(currentVersion32[:], genesisValidatorsRoot[:])
	return append(domainType, forkDataRoot[:28]...), nil
}

func ComputeSigningRoot(
	obj ssz.HashableSSZ,
	domain []byte,
) ([32]byte, error) {
	objRoot, err := obj.HashSSZ()
	if err != nil {
		return [32]byte{}, err
	}
	return utils.Sha256(objRoot[:], domain), nil
}

func Domain(fork *cltypes.Fork, epoch uint64, domainType [4]byte, genesisRoot common.Hash) ([]byte, error) {
	if fork == nil {
		return []byte{}, errors.New("nil fork or domain type")
	}
	var forkVersion []byte
	if epoch < fork.Epoch {
		forkVersion = fork.PreviousVersion[:]
	} else {
		forkVersion = fork.CurrentVersion[:]
	}
	if len(forkVersion) != 4 {
		return []byte{}, errors.New("fork version length is not 4 byte")
	}
	var forkVersionArray [4]byte
	copy(forkVersionArray[:], forkVersion[:4])
	return ComputeDomain(domainType[:], forkVersionArray, genesisRoot)
}
