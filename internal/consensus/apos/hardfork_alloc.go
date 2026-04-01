// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package apos

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// parsedAllocation holds a validated, pre-parsed allocation ready to apply.
type parsedAllocation struct {
	addr  types.Address
	value *uint256.Int
}

var (
	hardforkAllocOnce sync.Once
	hardforkAllocMap  map[uint64][]parsedAllocation // O(1) lookup by block
)

const hardforkAllocFile = "hardfork_alloc.json"

// SetHardForkAllocDir allows setting a custom search directory for the config
// file (e.g. the node's DataDir). If not called, CWD is used.
var hardforkAllocDir string

func SetHardForkAllocDir(dir string) { hardforkAllocDir = dir }

// validateAddress checks that the address is a valid 20-byte hex string.
func validateAddress(addr string) bool {
	s := strings.TrimPrefix(addr, "0x")
	s = strings.TrimPrefix(s, "0X")
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func loadHardForkAllocations() {
	hardforkAllocMap = make(map[uint64][]parsedAllocation)

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

	// Validate, parse, dedup-warn, and index by block number.
	seen := make(map[string]bool) // "block:address" dedup key
	for _, a := range raw {
		// Validate address format (must be exactly 20 bytes / 40 hex chars).
		if !validateAddress(a.Address) {
			log.Error("Invalid address in hardfork_alloc.json (must be 40 hex chars)",
				"block", a.Block, "address", a.Address)
			continue
		}
		// Parse and validate amount.
		value, err := uint256.FromHex(a.Amount)
		if err != nil {
			log.Error("Invalid hex amount in hardfork_alloc.json",
				"block", a.Block, "address", a.Address, "amount", a.Amount, "err", err)
			continue
		}
		// Warn on duplicates (same block+address).
		key := strings.ToLower(a.Address)
		dedupKey := string(rune(a.Block)) + ":" + key
		if seen[dedupKey] {
			log.Warn("Duplicate hardfork allocation (will apply both)",
				"block", a.Block, "address", a.Address)
		}
		seen[dedupKey] = true

		addr := types.HexToAddress(a.Address)
		hardforkAllocMap[a.Block] = append(hardforkAllocMap[a.Block], parsedAllocation{
			addr:  addr,
			value: value,
		})
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
		return
	}

	for _, a := range allocs {
		if !ibs.Exist(a.addr) {
			ibs.CreateAccount(a.addr, false)
		}
		ibs.AddBalance(a.addr, a.value)
		log.Debug("Hard-fork allocation applied",
			"block", blockNumber, "address", a.addr, "amount", a.value.ToBig())
	}
}
