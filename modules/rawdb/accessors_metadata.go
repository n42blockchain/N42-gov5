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
//
// ChainConfig persistence keyed by genesis hash.
// ReadChainConfig/WriteChainConfig JSON-encode *params.ChainConfig
// into modules.ChainConfig under modules.ConfigKey(genesisHash).
// ReadChainConfig also runs params.NormalizeConsensus to migrate
// legacy chain parameter layouts on load.

package rawdb

import (
	"encoding/json"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

// ReadChainConfig retrieves the consensus settings based on the given genesis hash.
func ReadChainConfig(db kv.Getter, hash types.Hash) (*params.ChainConfig, error) {
	data, err := db.GetOne(modules.ChainConfig, modules.ConfigKey(hash))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ChainConfig from db: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("ChainConfig is empty")
	}
	var config params.ChainConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid chain config JSON: %w", err)
	}
	return params.NormalizeConsensus(&config), nil
}

// WriteChainConfig writes the chain config settings to the database.
func WriteChainConfig(db kv.RwTx, hash types.Hash, cfg *params.ChainConfig) error {
	if cfg == nil {
		return fmt.Errorf("chain config is nil")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to JSON encode chain config: %w", err)
	}
	if err := db.Put(modules.ChainConfig, modules.ConfigKey(hash), data); err != nil {
		return fmt.Errorf("failed to store chain config: %w", err)
	}
	return nil
}
