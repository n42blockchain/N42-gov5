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

package vm

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestDelegationCodeHashConsistency(t *testing.T) {
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	code1 := AddressToDelegation(addr1)
	code2 := AddressToDelegation(addr2)

	if bytes.Equal(code1, code2) {
		t.Error("Different addresses should produce different delegation codes")
	}

	code1Again := AddressToDelegation(addr1)
	if !bytes.Equal(code1, code1Again) {
		t.Error("Same address should produce same delegation code")
	}
}
