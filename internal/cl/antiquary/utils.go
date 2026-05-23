//go:build n42el

// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package antiquary

import (
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/depshim/beaconindicies"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/lib/kv"
)

func findNearestSlotBackwards(tx kv.Tx, cfg *clparams.BeaconChainConfig, slot uint64) (uint64, error) {
	canonicalRoot, err := beacon_indicies.ReadCanonicalBlockRoot(tx, slot)
	if err != nil {
		return 0, err
	}
	for (canonicalRoot == (common.Hash{}) && slot > 0) || slot%cfg.SlotsPerEpoch != 0 {
		slot--
		canonicalRoot, err = beacon_indicies.ReadCanonicalBlockRoot(tx, slot)
		if err != nil {
			return 0, err
		}
	}
	return slot, nil
}
