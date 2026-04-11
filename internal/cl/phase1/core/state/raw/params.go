// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package raw

type StateLeafIndex uint

// All position of all the leaves of the state merkle tree.
const (
	GenesisTimeLeafIndex                 StateLeafIndex = 0
	GenesisValidatorsRootLeafIndex       StateLeafIndex = 1
	SlotLeafIndex                        StateLeafIndex = 2
	ForkLeafIndex                        StateLeafIndex = 3
	LatestBlockHeaderLeafIndex           StateLeafIndex = 4
	BlockRootsLeafIndex                  StateLeafIndex = 5
	StateRootsLeafIndex                  StateLeafIndex = 6
	HistoricalRootsLeafIndex             StateLeafIndex = 7
	Eth1DataLeafIndex                    StateLeafIndex = 8
	Eth1DataVotesLeafIndex               StateLeafIndex = 9
	Eth1DepositIndexLeafIndex            StateLeafIndex = 10
	ValidatorsLeafIndex                  StateLeafIndex = 11
	BalancesLeafIndex                    StateLeafIndex = 12
	RandaoMixesLeafIndex                 StateLeafIndex = 13
	SlashingsLeafIndex                   StateLeafIndex = 14
	PreviousEpochParticipationLeafIndex  StateLeafIndex = 15
	CurrentEpochParticipationLeafIndex   StateLeafIndex = 16
	JustificationBitsLeafIndex           StateLeafIndex = 17
	PreviousJustifiedCheckpointLeafIndex StateLeafIndex = 18
	CurrentJustifiedCheckpointLeafIndex  StateLeafIndex = 19
	FinalizedCheckpointLeafIndex         StateLeafIndex = 20
	// Altair
	InactivityScoresLeafIndex     StateLeafIndex = 21
	CurrentSyncCommitteeLeafIndex StateLeafIndex = 22
	NextSyncCommitteeLeafIndex    StateLeafIndex = 23
	// Bellatrix
	LatestExecutionPayloadHeaderLeafIndex StateLeafIndex = 24
	// Capella
	NextWithdrawalIndexLeafIndex          StateLeafIndex = 25
	NextWithdrawalValidatorIndexLeafIndex StateLeafIndex = 26
	HistoricalSummariesLeafIndex          StateLeafIndex = 27
	// Electra
	DepositRequestsStartIndexLeafIndex     StateLeafIndex = 28
	DepositBalanceToConsumeLeafIndex       StateLeafIndex = 29
	ExitBalanceToConsumeLeafIndex          StateLeafIndex = 30
	EarliestExitEpochLeafIndex             StateLeafIndex = 31
	ConsolidationBalanceToConsumeLeafIndex StateLeafIndex = 32
	EarliestConsolidationEpochLeafIndex    StateLeafIndex = 33
	PendingDepositsLeafIndex               StateLeafIndex = 34
	PendingPartialWithdrawalsLeafIndex     StateLeafIndex = 35
	PendingConsolidationsLeafIndex         StateLeafIndex = 36
	// Fulu
	ProposerLookaheadLeafIndex StateLeafIndex = 37
)

const (
	StateLeafSizeDeneb   = 32
	StateLeafSizeElectra = 37
	StateLeafSizeFulu    = 38

	StateLeafSizeLatest = StateLeafSizeFulu

	LeafInitValue  = 0
	LeafCleanValue = 1
	LeafDirtyValue = 2
)
