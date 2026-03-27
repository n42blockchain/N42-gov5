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
	"bytes"
	"sync"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
)

// DerivableList is the input to DeriveSha.
type DerivableList = hash.DerivableList

// DeriveSha delegates to common/hash.DeriveSha (legacy, NilHash for empty).
func DeriveSha(list DerivableList) types.Hash {
	return hash.DeriveSha(list)
}

// DeriveShaV2 delegates to common/hash.DeriveShaV2 (EmptyRootHash for empty).
func DeriveShaV2(list DerivableList) types.Hash {
	return hash.DeriveShaV2(list)
}

// hasherPool and encodeBufferPool are kept for use by internal tests
// (blockchain_test.go). The production DeriveSha delegates to common/hash.
var hasherPool = sync.Pool{
	New: func() interface{} { return sha3.NewLegacyKeccak256() },
}
var encodeBufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

func encodeForDerive(list DerivableList, i int, buf *bytes.Buffer) []byte {
	buf.Reset()
	list.EncodeIndex(i, buf)
	return utils.Copy(buf.Bytes())
}
