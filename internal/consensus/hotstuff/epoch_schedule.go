// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// EpochSchedule holds a pre-planned sequence of validator set configurations.
// Loaded from epoch_schedule.json at node startup. Allows operators to plan
// validator set changes in advance without runtime RPC calls.
//
// Based on n42-26 Rust: crates/n42-node/src/epoch_schedule.rs
type EpochSchedule struct {
	// Entries maps epoch number → planned validator configuration.
	// Sorted by epoch for deterministic lookup.
	Entries []EpochScheduleEntry `json:"entries"`
}

// EpochScheduleEntry defines the validator set for a specific epoch.
type EpochScheduleEntry struct {
	Epoch      uint64                   `json:"epoch"`
	Validators []EpochScheduleValidator `json:"validators"`
}

// EpochScheduleValidator holds a validator address and BLS public key hex.
type EpochScheduleValidator struct {
	Address string `json:"address"` // Hex address
	BLSKey  string `json:"blsKey"`  // Hex BLS12-381 public key (48 bytes)
}

// LoadEpochSchedule loads an epoch schedule from a JSON file.
// Returns nil (not error) if the file does not exist.
func LoadEpochSchedule(path string) (*EpochSchedule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read epoch schedule: %w", err)
	}

	var schedule EpochSchedule
	if err := json.Unmarshal(data, &schedule); err != nil {
		return nil, fmt.Errorf("parse epoch schedule: %w", err)
	}

	// Sort by epoch for deterministic lookup
	sort.Slice(schedule.Entries, func(i, j int) bool {
		return schedule.Entries[i].Epoch < schedule.Entries[j].Epoch
	})

	log.Info("Loaded epoch schedule", "entries", len(schedule.Entries))
	return &schedule, nil
}

// GetForEpoch returns the validator configuration for the given epoch.
// Uses the latest entry whose epoch <= target (BTreeMap::range semantics).
// Returns nil if no entry applies.
func (es *EpochSchedule) GetForEpoch(epoch uint64) *EpochScheduleEntry {
	if es == nil || len(es.Entries) == 0 {
		return nil
	}

	var best *EpochScheduleEntry
	for i := range es.Entries {
		if es.Entries[i].Epoch <= epoch {
			best = &es.Entries[i]
		} else {
			break // sorted, no need to continue
		}
	}
	return best
}

// ParseValidators converts an EpochScheduleEntry into ValidatorInfo slice.
func (entry *EpochScheduleEntry) ParseValidators() ([]ValidatorInfo, error) {
	validators := make([]ValidatorInfo, len(entry.Validators))
	for i, v := range entry.Validators {
		addr := types.HexToAddress(v.Address)
		pkBytes := types.Hex2Bytes(stripHexPrefix(v.BLSKey))
		pk, err := bls.PublicKeyFromBytes(pkBytes)
		if err != nil {
			return nil, fmt.Errorf("validator %d BLS key: %w", i, err)
		}
		validators[i] = ValidatorInfo{
			Address:   addr,
			PublicKey: pk,
		}
	}
	return validators, nil
}

// RecoveryWindow returns entries for epochs [startEpoch, endEpoch] for
// rebuilding EpochManager history on node restart.
func (es *EpochSchedule) RecoveryWindow(startEpoch, endEpoch uint64) []EpochScheduleEntry {
	if es == nil {
		return nil
	}
	var result []EpochScheduleEntry
	for _, e := range es.Entries {
		if e.Epoch >= startEpoch && e.Epoch <= endEpoch {
			result = append(result, e)
		}
	}
	return result
}

func stripHexPrefix(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}
