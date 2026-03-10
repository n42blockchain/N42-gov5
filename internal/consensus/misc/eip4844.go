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

package misc

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/params"
)

// VerifyEIP4844Header verifies the blob gas fields in the block header.
// It checks that BlobGasUsed is within limits and ExcessBlobGas is correctly
// derived from the parent header.
func VerifyEIP4844Header(parent, header *block.Header) error {
	// Verify blob gas used does not exceed maximum.
	if header.BlobGasUsed > params.MaxBlobGasPerBlock {
		return fmt.Errorf("blob gas used %d exceeds maximum %d",
			header.BlobGasUsed, params.MaxBlobGasPerBlock)
	}

	// Verify blob gas used is a multiple of gas per blob.
	if header.BlobGasUsed%params.BlobTxBlobGasPerBlob != 0 {
		return fmt.Errorf("blob gas used %d is not a multiple of %d",
			header.BlobGasUsed, params.BlobTxBlobGasPerBlob)
	}

	// Verify excess blob gas is correctly computed from parent.
	expectedExcess := transaction.CalcExcessBlobGas(parent.ExcessBlobGas, parent.BlobGasUsed)
	if header.ExcessBlobGas != expectedExcess {
		return fmt.Errorf("incorrect excess blob gas: have %d, want %d",
			header.ExcessBlobGas, expectedExcess)
	}

	return nil
}

// CalcBlobGasUsed computes the total blob gas consumed by blob transactions
// in a block.
func CalcBlobGasUsed(txs []*transaction.Transaction) uint64 {
	var blobGasUsed uint64
	for _, tx := range txs {
		gas := tx.BlobGas()
		if blobGasUsed+gas < blobGasUsed {
			// Overflow protection: cap at max uint64.
			return ^uint64(0)
		}
		blobGasUsed += gas
	}
	return blobGasUsed
}
