// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Utils unit for the eth2 package.
// Part of the n42el consensus-layer build.
// Part of the n42el consensus-layer build.

//go:build n42el

package eth2

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

func computeSigningRootEpoch(epoch uint64, domain []byte) (common.Hash, error) {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint64(b, epoch)
	return utils.Sha256(b, domain), nil
}

// transitionSlot is called each time there is a new slot to process
func transitionSlot(s abstract.BeaconState) error {
	slot := s.Slot()
	previousStateRoot := s.PreviousStateRoot()
	var err error
	if previousStateRoot == (common.Hash{}) {
		previousStateRoot, err = s.HashSSZ()
		if err != nil {
			return err
		}
	}

	beaconConfig := s.BeaconConfig()

	s.SetStateRootAt(int(slot%beaconConfig.SlotsPerHistoricalRoot), previousStateRoot)

	latestBlockHeader := s.LatestBlockHeader()
	if latestBlockHeader.Root == [32]byte{} {
		latestBlockHeader.Root = previousStateRoot
		s.SetLatestBlockHeader(&latestBlockHeader)
	}
	blockHeader := s.LatestBlockHeader()

	previousBlockRoot, err := (&blockHeader).HashSSZ()
	if err != nil {
		return err
	}
	s.SetBlockRootAt(int(slot%beaconConfig.SlotsPerHistoricalRoot), previousBlockRoot)
	return nil
}
