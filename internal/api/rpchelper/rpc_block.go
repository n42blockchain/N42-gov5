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
// Latest, finalized and safe block number accessors for RPC tags.
// GetLatestBlockNumber reads the current block header via rawdb.
// GetFinalizedBlockNumber and GetSafeBlockNumber currently alias to
// latest as placeholders until finality and safety tracking land,
// matching the semantics expected by eth_getBlockByNumber tag resolution.

package rpchelper

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func GetLatestBlockNumber(tx kv.Tx) (*uint256.Int, error) {
	current := rawdb.ReadCurrentBlock(tx)
	if current == nil {
		return nil, fmt.Errorf("cannot get current block")
	}
	return current.Number64(), nil
}

// GetFinalizedBlockNumber returns the finalized block number.
// Currently returns latest block; proper finality tracking needs implementation.
func GetFinalizedBlockNumber(tx kv.Tx) (*uint256.Int, error) {
	return GetLatestBlockNumber(tx)
}

// GetSafeBlockNumber returns the safe block number.
// Currently returns latest block; proper safe block tracking needs implementation.
func GetSafeBlockNumber(tx kv.Tx) (*uint256.Int, error) {
	return GetLatestBlockNumber(tx)
}
