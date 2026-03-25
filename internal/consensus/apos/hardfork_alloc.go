// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package apos

import (
	"encoding/json"
	"os"
	"path/filepath"
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

var (
	hardforkAllocOnce sync.Once
	hardforkAllocMap  map[uint64][]HardForkAllocation // O(1) lookup by block
)

const hardforkAllocFile = "hardfork_alloc.json"

// SetHardForkAllocDir allows setting a custom search directory for the config
// file (e.g. the node's DataDir). If not called, CWD is used.
var hardforkAllocDir string

func SetHardForkAllocDir(dir string) { hardforkAllocDir = dir }

func loadHardForkAllocations() {
	hardforkAllocMap = make(map[uint64][]HardForkAllocation)

	// Search in configured dir first, then CWD.
	paths := []string{hardforkAllocFile}
	if hardforkAllocDir != "" {
		paths = append([]string{filepath.Join(hardforkAllocDir, hardforkAllocFile)}, paths...)
	}

	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("No hardfork_alloc.json found (optional)")
		} else {
			log.Error("Failed to read hardfork_alloc.json", "err", err)
		}
		return
	}

	var raw []HardForkAllocation
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Error("Failed to parse hardfork_alloc.json", "err", err)
		return
	}

	// Validate hex amounts eagerly and index by block number.
	for _, a := range raw {
		if _, err := uint256.FromHex(a.Amount); err != nil {
			log.Error("Invalid hex amount in hardfork_alloc.json",
				"block", a.Block, "address", a.Address, "amount", a.Amount, "err", err)
			continue
		}
		hardforkAllocMap[a.Block] = append(hardforkAllocMap[a.Block], a)
	}

	if len(hardforkAllocMap) > 0 {
		log.Info("Loaded hard-fork allocations", "entries", len(raw), "blocks", len(hardforkAllocMap))
	}
}

// applyHardForkAllocations checks if any allocations should be applied at the
// given block height and modifies the state accordingly.
func applyHardForkAllocations(blockNumber uint64, ibs *state.IntraBlockState) {
	hardforkAllocOnce.Do(loadHardForkAllocations)

	allocs, ok := hardforkAllocMap[blockNumber]
	if !ok {
		return // O(1) — no allocations for this block
	}

	for _, alloc := range allocs {
		addr := types.HexToAddress(alloc.Address)
		value, _ := uint256.FromHex(alloc.Amount) // validated at load time
		if !ibs.Exist(addr) {
			ibs.CreateAccount(addr, false)
		}
		ibs.AddBalance(addr, value)
		log.Info("Hard-fork allocation applied",
			"block", blockNumber, "address", addr, "amount", value.ToBig())
	}
}
