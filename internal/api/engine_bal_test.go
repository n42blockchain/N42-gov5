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

package api

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestValidateBlockAccessList(t *testing.T) {
	t.Parallel()

	b := bal.NewBuilder()
	b.BalanceChange(1, types.HexToAddress("0x01"), uint256.NewInt(5))
	b.StorageRead(types.HexToAddress("0x02"), types.HexToHash("0x03"))
	list := b.Build() // three items
	hash, err := list.Hash()
	require.NoError(t, err)

	header := &block.Header{GasLimit: 3 * bal.ItemCost, BlockAccessListHash: &hash}
	require.NoError(t, validateBlockAccessList(list, header))

	other := types.HexToHash("0x04")
	require.ErrorContains(t, validateBlockAccessList(list, &block.Header{GasLimit: header.GasLimit, BlockAccessListHash: &other}), "hash mismatch")
	require.ErrorContains(t, validateBlockAccessList(list, &block.Header{GasLimit: header.GasLimit}), "hash mismatch")

	header.GasLimit--
	require.ErrorContains(t, validateBlockAccessList(list, header), "items")
}
