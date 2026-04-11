// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Validation unit for the eth2 package.
// Exports helpers such as VerifyTransition, VerifyBlockSignature, and
// VerifyBlockSignature.
// Part of the n42el consensus-layer build.

//go:build n42el

package eth2

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/fork"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/utils/bls"
)

func (I *impl) VerifyTransition(s abstract.BeaconState, currentBlock *cltypes.BeaconBlock) error {
	if !I.FullValidation {
		return nil
	}
	expectedStateRoot, err := s.HashSSZ()
	if err != nil {
		return fmt.Errorf("unable to generate state root: %v", err)
	}
	if expectedStateRoot != currentBlock.StateRoot {
		return fmt.Errorf("expected state root differs from received state root, slot %d , we have %s, ans %s", s.Slot(), hex.EncodeToString(expectedStateRoot[:]), hex.EncodeToString(currentBlock.StateRoot[:]))
	}
	return nil
}

func (I *impl) VerifyBlockSignature(s abstract.BeaconState, block *cltypes.SignedBeaconBlock) error {
	if !I.FullValidation {
		return nil
	}
	valid, err := VerifyBlockSignature(s, block)
	if err != nil {
		return fmt.Errorf("error validating block signature: %v", err)
	}
	if !valid {
		return errors.New("block not valid")
	}
	return nil
}

func VerifyBlockSignature(s abstract.BeaconState, block *cltypes.SignedBeaconBlock) (bool, error) {
	proposer, err := s.ValidatorForValidatorIndex(int(block.Block.ProposerIndex))
	if err != nil {
		return false, err
	}
	domain, err := s.GetDomain(s.BeaconConfig().DomainBeaconProposer, state.Epoch(s))
	if err != nil {
		return false, err
	}
	sigRoot, err := fork.ComputeSigningRoot(block.Block, domain)
	if err != nil {
		return false, err
	}
	pk := proposer.PublicKey()
	return bls.Verify(block.Signature[:], sigRoot[:], pk[:])
}
