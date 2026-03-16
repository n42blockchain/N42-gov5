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
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

func TestVerifyEip1559HeaderRejectsMissingParentNumber(t *testing.T) {
	config := &params.ChainConfig{LondonBlock: big.NewInt(0)}
	header := &block.Header{
		GasLimit: params.MinGasLimit,
		BaseFee:  uint256.NewInt(params.InitialBaseFee),
	}

	err := VerifyEip1559Header(config, &block.Header{GasLimit: params.MinGasLimit}, header)
	if err == nil || err.Error() != "parent header number unavailable" {
		t.Fatalf("VerifyEip1559Header() error = %v, want parent header number unavailable", err)
	}
}

func TestCalcBaseFeeMissingParentNumberReturnsInitialBaseFee(t *testing.T) {
	config := &params.ChainConfig{LondonBlock: big.NewInt(0)}

	baseFee := CalcBaseFee(config, &block.Header{BaseFee: uint256.NewInt(1)})
	if baseFee.Uint64() != params.InitialBaseFee {
		t.Fatalf("CalcBaseFee() = %d, want %d", baseFee.Uint64(), params.InitialBaseFee)
	}
}
