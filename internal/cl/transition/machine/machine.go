// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Machine unit for the machine package.
// Defines the Interface, BlockProcessor, BlockValidator, and SlotProcessor
// types.
// Part of the n42el consensus-layer build.

// Package machine is the interface for eth2 state transition
//go:build n42el

package machine

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

type Interface interface {
	BlockValidator
	BlockProcessor
	SlotProcessor
}

type BlockProcessor interface {
	BlockHeaderProcessor
	BlockOperationProcessor
}

type BlockValidator interface {
	VerifyBlockSignature(s abstract.BeaconState, block *cltypes.SignedBeaconBlock) error
	VerifyTransition(s abstract.BeaconState, block *cltypes.BeaconBlock) error
}

type SlotProcessor interface {
	ProcessSlots(s abstract.BeaconState, slot uint64) error
}

type BlockHeaderProcessor interface {
	ProcessBlockHeader(s abstract.BeaconState, slot, proposerIndex uint64, parentRoot common.Hash, bodyRoot [32]byte) error
	ProcessWithdrawals(s abstract.BeaconState, withdrawals *solid.ListSSZ[*cltypes.Withdrawal]) error
	ProcessExecutionPayload(s abstract.BeaconState, body cltypes.GenericBeaconBody) error
	ProcessRandao(s abstract.BeaconState, randao [96]byte, proposerIndex uint64) error
	ProcessEth1Data(state abstract.BeaconState, eth1Data *cltypes.Eth1Data) error
	ProcessSyncAggregate(s abstract.BeaconState, sync *cltypes.SyncAggregate) error
}

type BlockOperationProcessor interface {
	ProcessProposerSlashing(s abstract.BeaconState, propSlashing *cltypes.ProposerSlashing) error
	ProcessAttesterSlashing(s abstract.BeaconState, attSlashing *cltypes.AttesterSlashing) error
	ProcessAttestations(s abstract.BeaconState, attestations *solid.ListSSZ[*solid.Attestation]) error
	ProcessDeposit(s abstract.BeaconState, deposit *cltypes.Deposit) error
	ProcessVoluntaryExit(s abstract.BeaconState, signedVoluntaryExit *cltypes.SignedVoluntaryExit) error
	ProcessBlsToExecutionChange(state abstract.BeaconState, signedChange *cltypes.SignedBLSToExecutionChange) error
	ProcessDepositRequest(s abstract.BeaconState, depositRequest *solid.DepositRequest) error
	ProcessWithdrawalRequest(s abstract.BeaconState, withdrawalRequest *solid.WithdrawalRequest) error
	ProcessConsolidationRequest(s abstract.BeaconState, consolidationRequest *solid.ConsolidationRequest) error
	FullValidate() bool
}
