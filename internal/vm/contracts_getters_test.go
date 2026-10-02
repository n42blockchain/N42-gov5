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

// Covers the Registry precompile-getter factory functions in contracts.go
// (GetEcrecover, GetSha256, ...), which were previously only reached
// indirectly, if at all, through the fork precompile maps.

package vm

import "testing"

func TestPrecompileGetters(t *testing.T) {
	getters := map[string]func() PrecompiledContract{
		"ecrecover":  GetEcrecover,
		"sha256":     GetSha256,
		"ripemd160":  GetRipemd160,
		"dataCopy":   GetDataCopy,
		"blake2f":    GetBlake2F,
		"g1add":      GetBls12381G1Add,
		"g1mul":      GetBls12381G1Mul,
		"g1multiexp": GetBls12381G1MultiExp,
		"g2add":      GetBls12381G2Add,
		"g2mul":      GetBls12381G2Mul,
		"g2multiexp": GetBls12381G2MultiExp,
		"pairing":    GetBls12381Pairing,
		"mapG1":      GetBls12381MapG1,
		"mapG2":      GetBls12381MapG2,
	}
	for name, fn := range getters {
		t.Run(name, func(t *testing.T) {
			c := fn()
			if c == nil {
				t.Fatalf("%s getter returned nil", name)
			}
			// Touch RequiredGas to prove the returned value is a usable
			// PrecompiledContract, not just a non-nil interface wrapping a
			// nil pointer.
			_ = c.RequiredGas(nil)
		})
	}
}

func TestGetBigModExpVariants(t *testing.T) {
	tests := []struct {
		name                       string
		eip2565, eip7823, eip7883 bool
	}{
		{"legacy", false, false, false},
		{"eip2565_only", true, false, false},
		{"osaka_all", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := GetBigModExp(tt.eip2565, tt.eip7823, tt.eip7883)
			if c == nil {
				t.Fatalf("GetBigModExp returned nil")
			}
		})
	}
}

func TestGetBn256AddScalarMulPairingForkSelection(t *testing.T) {
	if GetBn256Add(true) == nil || GetBn256Add(false) == nil {
		t.Fatal("GetBn256Add returned nil")
	}
	if GetBn256ScalarMul(true) == nil || GetBn256ScalarMul(false) == nil {
		t.Fatal("GetBn256ScalarMul returned nil")
	}
	if GetBn256Pairing(true) == nil || GetBn256Pairing(false) == nil {
		t.Fatal("GetBn256Pairing returned nil")
	}
}

func TestGetAIInferenceReturnsUsableInstance(t *testing.T) {
	c := GetAIInference()
	if c == nil {
		t.Fatal("GetAIInference returned nil")
	}
	if got := c.RequiredGas([]byte{aiGetResult}); got != AIInferenceGetResultGas {
		t.Errorf("RequiredGas via GetAIInference() = %d, want %d", got, AIInferenceGetResultGas)
	}
}
