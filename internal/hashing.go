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
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// DerivableList is the input to DeriveSha.
// Re-exported from common/hash for use within the internal package.
type DerivableList = hash.DerivableList

// DeriveSha delegates to common/hash.DeriveSha (legacy, NilHash for empty).
func DeriveSha(list DerivableList) types.Hash {
	return hash.DeriveSha(list)
}

// DeriveShaV2 delegates to common/hash.DeriveShaV2 (EmptyRootHash for empty).
func DeriveShaV2(list DerivableList) types.Hash {
	return hash.DeriveShaV2(list)
}
