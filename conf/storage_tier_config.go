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

package conf

import (
	"fmt"
	"os"
	"path/filepath"
)

// StorageTierCfg allows operators to place hot data on NVMe and cold data
// on HDD, reducing hardware costs. When Enabled, the specified paths override
// the default data directory layout.
type StorageTierCfg struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	HotPath        string `json:"hot_path" yaml:"hot_path"`                 // NVMe: chaindata MDBX (~50GB)
	WarmPath       string `json:"warm_path" yaml:"warm_path"`               // NVMe recommended: domain snapshots (~300GB)
	ColdPath       string `json:"cold_path" yaml:"cold_path"`               // HDD acceptable: history, indices, freezer (~800GB)
	DownloaderPath string `json:"downloader_path" yaml:"downloader_path"`   // follows ColdPath by default
}

// DefaultStorageTierCfg returns a disabled storage tier configuration.
func DefaultStorageTierCfg() StorageTierCfg {
	return StorageTierCfg{Enabled: false}
}

// Validate checks that configured paths can be created.
// Empty paths are allowed (they simply won't override the defaults).
func (c *StorageTierCfg) Validate() error {
	if !c.Enabled {
		return nil
	}

	// At least one path must be specified when tiering is enabled.
	if c.HotPath == "" && c.WarmPath == "" && c.ColdPath == "" && c.DownloaderPath == "" {
		return fmt.Errorf("storage_tier: enabled but no tier paths configured")
	}

	// Validate each non-empty path is an absolute path and its parent is
	// writable (or can be created).
	for name, p := range map[string]string{
		"hot_path":        c.HotPath,
		"warm_path":       c.WarmPath,
		"cold_path":       c.ColdPath,
		"downloader_path": c.DownloaderPath,
	} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("storage_tier: %s: %w", name, err)
		}
		// Try to create the directory to verify it is writable.
		if err := os.MkdirAll(abs, 0700); err != nil {
			return fmt.Errorf("storage_tier: cannot create %s %q: %w", name, abs, err)
		}
	}

	return nil
}
