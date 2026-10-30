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
	"fmt"

	"github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/block"
)

// validateBlockAccessList checks the EIP-7928 access list built by executing
// an Amsterdam block: it must fit the size constraint of the block gas limit
// and hash to the header's block access list hash.
func validateBlockAccessList(list bal.BlockAccessList, header *block.Header) error {
	if err := list.ValidateSize(header.GasLimit); err != nil {
		return err
	}
	got, err := list.Hash()
	if err != nil {
		return fmt.Errorf("encode block access list: %w", err)
	}
	if want := header.BlockAccessListHash; want == nil || *want != got {
		return fmt.Errorf("block access list hash mismatch: header %v, computed %s", want, got.Hex())
	}
	return nil
}
