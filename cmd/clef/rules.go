// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/types"
)

// RuleConfig represents a JSON-based rule configuration that determines
// which signing requests should be approved or denied. This provides a
// simple, auditable alternative to a full JavaScript rules engine.
//
// Example rules.json:
//
//	{
//	  "allow_addresses": ["0xaaaa...", "0xbbbb..."],
//	  "max_value_wei": "1000000000000000000",
//	  "require_to": true
//	}
type RuleConfig struct {
	// AllowAddresses restricts signing to only these sender addresses.
	// An empty list means all addresses are allowed.
	AllowAddresses []string `json:"allow_addresses"`

	// MaxValueWei is the maximum transaction value in wei. Transactions
	// exceeding this value are denied. Empty string or "0" means no limit.
	MaxValueWei string `json:"max_value_wei"`

	// RequireTo, when true, rejects transactions without a To address
	// (i.e. contract-creation transactions).
	RequireTo bool `json:"require_to"`
}

// RuleEngine evaluates signing requests against a declarative rule set
// loaded from a JSON configuration file. It is intentionally simple and
// avoids embedding a scripting runtime (no goja/JS dependency).
type RuleEngine struct {
	configPath     string
	config         RuleConfig
	allowSet       map[types.Address]struct{}
	maxValue       *big.Int // nil means no limit
}

// NewRuleEngine loads and parses the rules configuration from configPath.
// If configPath is empty, a permissive engine that approves everything is returned.
func NewRuleEngine(configPath string) (*RuleEngine, error) {
	re := &RuleEngine{
		configPath: configPath,
		allowSet:   make(map[types.Address]struct{}),
	}

	if configPath == "" {
		return re, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read rules config %s: %w", configPath, err)
	}

	if err := json.Unmarshal(data, &re.config); err != nil {
		return nil, fmt.Errorf("parse rules config %s: %w", configPath, err)
	}

	// Build the allowed address set.
	for _, hexAddr := range re.config.AllowAddresses {
		hexAddr = strings.TrimSpace(hexAddr)
		if hexAddr == "" {
			continue
		}
		var addr types.Address
		if err := addr.UnmarshalText([]byte(hexAddr)); err != nil {
			return nil, fmt.Errorf("invalid allow_address %q: %w", hexAddr, err)
		}
		re.allowSet[addr] = struct{}{}
	}

	// Parse the maximum value.
	if re.config.MaxValueWei != "" && re.config.MaxValueWei != "0" {
		val, ok := new(big.Int).SetString(re.config.MaxValueWei, 10)
		if !ok {
			return nil, fmt.Errorf("invalid max_value_wei %q: not a valid integer", re.config.MaxValueWei)
		}
		if val.Sign() < 0 {
			return nil, fmt.Errorf("invalid max_value_wei %q: must be non-negative", re.config.MaxValueWei)
		}
		re.maxValue = val
	}

	return re, nil
}

// ApproveTransaction evaluates whether a transaction should be signed.
// It returns (true, "") if approved, or (false, reason) if denied.
func (re *RuleEngine) ApproveTransaction(tx *TransactionArgs) (bool, string) {
	if tx == nil {
		return false, "nil transaction"
	}

	// Check allow-list.
	if len(re.allowSet) > 0 {
		if _, ok := re.allowSet[tx.From]; !ok {
			return false, fmt.Sprintf("sender %s not in allow_addresses", tx.From.Hex())
		}
	}

	// Check require_to.
	if re.config.RequireTo && tx.To == nil {
		return false, "contract-creation transactions are not allowed (require_to=true)"
	}

	// Check max value.
	if re.maxValue != nil && tx.Value != nil {
		txValue := tx.Value.ToInt()
		if txValue.Cmp(re.maxValue) > 0 {
			return false, fmt.Sprintf("transaction value %s exceeds max_value_wei %s",
				txValue.String(), re.maxValue.String())
		}
	}

	return true, ""
}

// ApproveSignData evaluates whether arbitrary data signing should be approved
// for the given address. Currently this only checks the address allow-list.
func (re *RuleEngine) ApproveSignData(addr types.Address, data []byte) (bool, string) {
	if len(re.allowSet) > 0 {
		if _, ok := re.allowSet[addr]; !ok {
			return false, fmt.Sprintf("address %s not in allow_addresses", addr.Hex())
		}
	}
	return true, ""
}
