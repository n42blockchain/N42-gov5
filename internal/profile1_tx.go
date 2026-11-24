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

package internal

import (
	"fmt"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/params"
)

// CheckProfile1TxType refuses a transaction type that is not valid on a
// Standard Profile 1 chain (standard section 2(c), C1-C4). It is the one
// rule the pool, the builder, block validity, the deferred includability
// check and execution apply; a chain outside the profile is never refused
// here. The error wraps ErrTxTypeNotSupported and ErrTxTypeNotInProfile1,
// and ErrBlobTxOnNativeChain for type 3.
func CheckProfile1TxType(config *params.ChainConfig, typ uint8) error {
	if !config.IsProfile1() || transaction.Profile1TxTypeAllowed(typ) {
		return nil
	}
	if typ == transaction.BlobTxType {
		return fmt.Errorf("%w: %w: %w", ErrTxTypeNotSupported, ErrTxTypeNotInProfile1, ErrBlobTxOnNativeChain)
	}
	return fmt.Errorf("%w: %w: type 0x%02x", ErrTxTypeNotSupported, ErrTxTypeNotInProfile1, typ)
}
