// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// genesis.go — Ethereum genesis alloc loader for eth-el chaindata.
//
// InitEthGenesisState and InitEthGenesisStateFromBytes parse a standard
// mainnet genesis JSON (alloc section with balance, nonce, code and
// storage slots), decode hex-prefixed addresses and values, and write the
// initial state into the MDBX Account/Code/Storage tables using the Reth-
// style plain layout. IsGenesisInitialized is idempotent so running the
// loader on an already-populated database is a safe no-op.

package ethel

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// genesisAllocEntry matches the standard Ethereum genesis alloc JSON format.
type genesisAllocEntry struct {
	Balance string                     `json:"balance"`
	Code    string                     `json:"code,omitempty"`
	Nonce   uint64                     `json:"nonce,omitempty"`
	Storage map[types.Hash]types.Hash  `json:"storage,omitempty"`
}

// IsGenesisInitialized checks if the database already has genesis state.
func IsGenesisInitialized(tx kv.Tx) bool {
	cursor, err := tx.Cursor("Account")
	if err != nil {
		return false
	}
	defer cursor.Close()
	k, _, err := cursor.First()
	return k != nil && err == nil
}

// InitEthGenesisState loads an Ethereum genesis alloc JSON file and writes
// the initial state to the MDBX database. Skips if state already exists.
func InitEthGenesisState(tx kv.RwTx, genesisPath string) (int, error) {
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		return 0, fmt.Errorf("read genesis: %w", err)
	}
	return InitEthGenesisStateFromBytes(tx, data)
}

// InitEthGenesisStateFromBytes loads genesis state from JSON bytes.
func InitEthGenesisStateFromBytes(tx kv.RwTx, data []byte) (int, error) {
	if IsGenesisInitialized(tx) {
		log.Info("Genesis state already initialized, skipping")
		return 0, nil
	}

	// Parse the alloc section.
	var genesis struct {
		Alloc map[string]genesisAllocEntry `json:"alloc"`
	}
	if err := json.Unmarshal(data, &genesis); err != nil {
		return 0, fmt.Errorf("parse genesis JSON: %w", err)
	}

	if len(genesis.Alloc) == 0 {
		// Try parsing as a raw alloc map (without "alloc" wrapper).
		var rawAlloc map[string]genesisAllocEntry
		if err := json.Unmarshal(data, &rawAlloc); err != nil {
			return 0, fmt.Errorf("parse genesis alloc: %w", err)
		}
		genesis.Alloc = rawAlloc
	}

	writer := state.NewPlainStateWriter(tx, tx, 0)
	ibs := state.New(state.NewPlainStateReader(tx))

	count := 0
	for addrHex, entry := range genesis.Alloc {
		addr := types.HexToAddress(addrHex)

		// Parse balance.
		balance := new(uint256.Int)
		if entry.Balance != "" {
			b, ok := new(big.Int).SetString(entry.Balance, 0) // supports 0x prefix and decimal
			if !ok {
				return 0, fmt.Errorf("invalid balance for %s: %s", addrHex, entry.Balance)
			}
			balance.SetFromBig(b)
		}

		ibs.CreateAccount(addr, true)
		ibs.SetBalance(addr, balance)

		if entry.Nonce > 0 {
			ibs.SetNonce(addr, entry.Nonce)
		}

		if entry.Code != "" {
			code, _ := hex.DecodeString(strings.TrimPrefix(entry.Code, "0x"))
			ibs.SetCode(addr, code)
		}

		for key, value := range entry.Storage {
			v := new(uint256.Int)
			v.SetBytes(value.Bytes())
			ibs.SetState(addr, &key, *v)
		}

		count++
	}

	// Commit to writer.
	rules := &params.Rules{}
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return 0, fmt.Errorf("commit genesis state: %w", err)
	}
	if err := writer.WriteChangeSets(); err != nil {
		return 0, fmt.Errorf("write genesis changesets: %w", err)
	}
	if err := writer.WriteHistory(); err != nil {
		return 0, fmt.Errorf("write genesis history: %w", err)
	}

	log.Info("Initialized ETH genesis state", "accounts", count)
	return count, nil
}
