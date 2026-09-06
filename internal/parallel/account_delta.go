// Copyright 2026 The N42 Authors
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

package parallel

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
)

// composeAccount returns full with delta added to its balance, in the
// account's storage encoding. A nil full with a delta is a fresh account
// holding the delta; a nil delta returns full as is.
func composeAccount(full []byte, delta *uint256.Int) []byte {
	if delta == nil {
		return full
	}
	acc := account.StateAccount{Initialised: true}
	if full != nil {
		if err := acc.DecodeForStorageV2(full); err != nil {
			return full
		}
	}
	acc.Balance.Add(&acc.Balance, delta)
	return acc.MarshalV2()
}

// accountEqualIgnoringBalance compares two encoded accounts on every field
// but the balance; nil is an absent account and equals only nil.
func accountEqualIgnoringBalance(a, b []byte) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var x, y account.StateAccount
	if err := x.DecodeForStorageV2(a); err != nil {
		return false
	}
	if err := y.DecodeForStorageV2(b); err != nil {
		return false
	}
	return x.Nonce == y.Nonce && x.CodeHash == y.CodeHash && x.Root == y.Root
}
