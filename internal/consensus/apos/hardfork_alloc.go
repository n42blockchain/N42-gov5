// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package apos

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// HardForkAllocation defines a balance injection at a specific block height.
type HardForkAllocation struct {
	Block   uint64 `json:"block"`
	Address string `json:"address"`
	Amount  string `json:"amount"` // hex uint256 (e.g. "0x9B18AB5DF7180B6B8000000")
}

// hardforkAllocConfig is loaded once from the external JSON file.
var (
	hardforkAllocOnce   sync.Once
	hardforkAllocations []HardForkAllocation
)

const hardforkAllocFile = "hardfork_alloc.json"

// loadHardForkAllocations reads the allocation config from hardfork_alloc.json.
// The file is optional — if not found, no allocations are applied.
func loadHardForkAllocations() {
	data, err := os.ReadFile(hardforkAllocFile)
	if err != nil {
		// File not found is normal (no hard-fork allocations configured).
		return
	}
	if err := json.Unmarshal(data, &hardforkAllocations); err != nil {
		log.Error("Failed to parse hardfork_alloc.json", "err", err)
		return
	}
	if len(hardforkAllocations) > 0 {
		log.Info("Loaded hard-fork allocations", "count", len(hardforkAllocations))
	}
}

// applyHardForkAllocations checks if any allocations should be applied at the
// given block height and modifies the state accordingly.
func applyHardForkAllocations(blockNumber uint64, ibs *state.IntraBlockState) {
	hardforkAllocOnce.Do(loadHardForkAllocations)

	for _, alloc := range hardforkAllocations {
		if alloc.Block != blockNumber {
			continue
		}
		addr := types.HexToAddress(alloc.Address)
		value, err := uint256.FromHex(alloc.Amount)
		if err != nil {
			log.Error("Invalid hard-fork allocation amount", "address", alloc.Address, "amount", alloc.Amount, "err", err)
			continue
		}
		if !ibs.Exist(addr) {
			ibs.CreateAccount(addr, false)
		}
		ibs.AddBalance(addr, value)
		log.Info("Hard-fork allocation applied", "block", blockNumber, "address", addr, "amount", value.ToBig())
	}
}
