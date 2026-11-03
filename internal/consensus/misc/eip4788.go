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
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

// ErrMissingBeaconRoot is returned for a Cancun header without the EIP-4788
// parentBeaconBlockRoot field (geth consensus/beacon wording).
var ErrMissingBeaconRoot = errors.New("header is missing beaconRoot")

// VerifyEIP4788Header rejects a header that has Cancun active but carries no
// parentBeaconBlockRoot. Block processing skips the EIP-4788 system call when
// the root is nil, so without this check such a block would execute with the
// call silently missing instead of being invalid.
func VerifyEIP4788Header(cfg *params.ChainConfig, header *block.Header) error {
	if cfg == nil || header == nil || header.Number == nil || header.ParentBeaconRoot != nil {
		return nil
	}
	if cfg.IsCancunAt(header.Number.Uint64(), header.Time) {
		return fmt.Errorf("block %d: %w", header.Number.Uint64(), ErrMissingBeaconRoot)
	}
	return nil
}
