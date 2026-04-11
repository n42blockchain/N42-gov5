// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Transition unit for the machine package.
// Exports helpers such as TransitionState.
// Part of the n42el consensus-layer build.

//go:build n42el

package machine

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
)

// TransitionState will call impl..ProcessSlots, then impl.VerifyBlockSignature, then ProcessBlock, then impl.VerifyTransition
func TransitionState(impl Interface, s abstract.BeaconState, block *cltypes.SignedBeaconBlock) error {
	currentBlock := block.Block
	if err := impl.ProcessSlots(s, currentBlock.Slot); err != nil {
		return err
	}

	if err := impl.VerifyBlockSignature(s, block); err != nil {
		return err
	}

	// Transition block
	if err := ProcessBlock(impl, s, block.Block); err != nil {
		return err
	}

	// perform validation
	if err := impl.VerifyTransition(s, currentBlock); err != nil {
		return err
	}

	// if validation is successful, transition
	s.SetPreviousStateRoot(currentBlock.StateRoot)
	return nil
}
