// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package kv is a thin re-export layer over N42's lib/kv that satisfies the
// Caplin (cl/) source tree's import path:
//
//	github.com/n42blockchain/N42/internal/cl/depshim/kv  →  github.com/n42blockchain/N42/internal/cl/depshim/kv
//
// N42's lib/kv was originally forked from erigon's db/kv, so the type and
// constant surface is identical for everything Caplin uses. This file
// re-exports the subset that the cl/ subtree references via Go type aliases
// (for types) and constant aliases (for bucket names and other tables.go
// values). No behavior is added, no behavior is changed — every symbol below
// is exactly the same value as the corresponding symbol in lib/kv.
//
// If a future Caplin update needs a kv.* symbol that is not yet re-exported
// here, the only required change is to add a new alias line below.
package kv

import libkv "github.com/n42blockchain/N42/lib/kv"

// --- Type aliases ---------------------------------------------------------

type (
	RoDB         = libkv.RoDB
	RwDB         = libkv.RwDB
	Tx           = libkv.Tx
	RwTx         = libkv.RwTx
	TableCfg     = libkv.TableCfg
	TableCfgItem = libkv.TableCfgItem
	Label        = libkv.Label
)

// --- Bucket-name and key constants used by cl/ ---------------------------

const (
	ActiveValidatorIndicies       = libkv.ActiveValidatorIndicies
	BalancesDump                  = libkv.BalancesDump
	BeaconBlockHeaders            = libkv.BeaconBlockHeaders
	BeaconBlocks                  = libkv.BeaconBlocks
	BlockRoot                     = libkv.BlockRoot
	BlockRootToBlockHash          = libkv.BlockRootToBlockHash
	BlockRootToBlockNumber        = libkv.BlockRootToBlockNumber
	BlockRootToKzgCommitments     = libkv.BlockRootToKzgCommitments
	BlockRootToParentRoot         = libkv.BlockRootToParentRoot
	BlockRootToSlot               = libkv.BlockRootToSlot
	BlockRootToStateRoot          = libkv.BlockRootToStateRoot
	CanonicalBlockRoots           = libkv.CanonicalBlockRoots
	CurrentSyncCommittee          = libkv.CurrentSyncCommittee
	EffectiveBalancesDump         = libkv.EffectiveBalancesDump
	EpochData                     = libkv.EpochData
	Eth1DataVotes                 = libkv.Eth1DataVotes
	Headers                       = libkv.Headers
	HighestFinalized              = libkv.HighestFinalized
	InactivityScores              = libkv.InactivityScores
	IntraRandaoMixes              = libkv.IntraRandaoMixes
	LastBeaconSnapshot            = libkv.LastBeaconSnapshot
	LastBeaconSnapshotKey         = libkv.LastBeaconSnapshotKey
	NextSyncCommittee             = libkv.NextSyncCommittee
	ParentRootToBlockRoots        = libkv.ParentRootToBlockRoots
	PendingConsolidations         = libkv.PendingConsolidations
	PendingConsolidationsDump     = libkv.PendingConsolidationsDump
	PendingDeposits               = libkv.PendingDeposits
	PendingDepositsDump           = libkv.PendingDepositsDump
	PendingPartialWithdrawals     = libkv.PendingPartialWithdrawals
	PendingPartialWithdrawalsDump = libkv.PendingPartialWithdrawalsDump
	RandaoMixes                   = libkv.RandaoMixes
	SlotData                      = libkv.SlotData
	StateEvents                   = libkv.StateEvents
	StateRoot                     = libkv.StateRoot
	StateRootToBlockRoot          = libkv.StateRootToBlockRoot
	StatesProcessingProgress      = libkv.StatesProcessingProgress
	StaticValidators              = libkv.StaticValidators
	ValidatorBalance              = libkv.ValidatorBalance
	ValidatorEffectiveBalance     = libkv.ValidatorEffectiveBalance
	ValidatorSlashings            = libkv.ValidatorSlashings
)

// --- Variable re-exports (cannot be const because they are []byte) -------

var (
	HighestFinalizedKey = libkv.HighestFinalizedKey
	StatesProcessingKey = libkv.StatesProcessingKey
)
