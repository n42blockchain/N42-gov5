// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Buckets unit for the kvadapter package.
// Part of the n42el consensus-layer build.
// Part of the n42el consensus-layer build.

//go:build n42el

package kvadapter

import (
	libkv "github.com/n42blockchain/N42/lib/kv"
	depkv "github.com/n42blockchain/N42/internal/cl/depshim/kv"
)

// CaplinTables is the bucket-name set Caplin reads and writes. Every name
// here must already be defined in N42's lib/kv/tables.go (lib/kv was forked
// from erigon's db/kv, so all bucket names match by construction).
//
// The list comes from depshim/kv re-exports — the single source of truth.
// If a future Caplin update introduces a new bucket, add it both to
// depshim/kv/kv.go and to this slice.
var CaplinTables = []string{
	depkv.ActiveValidatorIndicies,
	depkv.BalancesDump,
	depkv.BeaconBlockHeaders,
	depkv.BeaconBlocks,
	depkv.BlockRoot,
	depkv.BlockRootToBlockHash,
	depkv.BlockRootToBlockNumber,
	depkv.BlockRootToKzgCommitments,
	depkv.BlockRootToParentRoot,
	depkv.BlockRootToSlot,
	depkv.BlockRootToStateRoot,
	depkv.CanonicalBlockRoots,
	depkv.CurrentSyncCommittee,
	depkv.EffectiveBalancesDump,
	depkv.EpochData,
	depkv.Eth1DataVotes,
	depkv.Headers,
	depkv.HighestFinalized,
	depkv.InactivityScores,
	depkv.IntraRandaoMixes,
	depkv.LastBeaconSnapshot,
	depkv.NextSyncCommittee,
	depkv.ParentRootToBlockRoots,
	depkv.PendingConsolidations,
	depkv.PendingConsolidationsDump,
	depkv.PendingDeposits,
	depkv.PendingDepositsDump,
	depkv.PendingPartialWithdrawals,
	depkv.PendingPartialWithdrawalsDump,
	depkv.RandaoMixes,
	depkv.SlotData,
	depkv.StateEvents,
	depkv.StateRoot,
	depkv.StateRootToBlockRoot,
	depkv.StatesProcessingProgress,
	depkv.StaticValidators,
	depkv.ValidatorBalance,
	depkv.ValidatorEffectiveBalance,
	depkv.ValidatorSlashings,
}

// buildTableCfg is the WithTableCfg callback. It receives the lib/kv default
// table set (which is the full chaindata table set used by N42 native +
// EthEL) and replaces it with the Caplin-only subset. This guarantees the
// MDBX environment exposes ONLY tables Caplin owns — accidental writes to
// EL tables will fail with "bucket does not exist".
func buildTableCfg(_ libkv.TableCfg) libkv.TableCfg {
	cfg := make(libkv.TableCfg, len(CaplinTables))
	for _, name := range CaplinTables {
		cfg[name] = libkv.TableCfgItem{}
	}
	return cfg
}
