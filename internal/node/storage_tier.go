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
// Storage tier configuration helper. Maps conf.Config storage
// directives onto MDBX + freezer layout, setting up hot/cold paths
// and applying per-table retention rules read from the node
// configuration file.

package node

import (
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/log"
)

// applyStorageTier applies tiered storage configuration to the data directory layout.
// Must be called before any database or freezer is opened.
func applyStorageTier(cfg *conf.StorageTierCfg, dirs *datadir.Dirs) error {
	if !cfg.Enabled {
		return nil
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Validate() already ensures tier directories exist via MkdirAll.
	datadir.ApplyTierOverrides(dirs, cfg.HotPath, cfg.WarmPath, cfg.ColdPath, cfg.DownloaderPath)

	log.Info("Storage tiering applied",
		"hot", cfg.HotPath,
		"warm", cfg.WarmPath,
		"cold", cfg.ColdPath,
		"downloader", cfg.DownloaderPath,
		"chaindata", dirs.Chaindata,
		"snapDomain", dirs.SnapDomain,
		"snapHistory", dirs.SnapHistory,
		"downloaderDir", dirs.Downloader,
	)

	return nil
}
