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

package params

import (
	"math/big"
	"testing"
)

func TestGlamsterdamConstants(t *testing.T) {
	tests := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"TxGasGlamsterdam", TxGasGlamsterdam, 4500},
		{"TxGasContractCreationGlamsterdam", TxGasContractCreationGlamsterdam, 12500},
		{"TxDataZeroGasGlamsterdam", TxDataZeroGasGlamsterdam, 1},
		{"TxDataNonZeroGasGlamsterdam", TxDataNonZeroGasGlamsterdam, 4},
		{"TxAccessListAddressGasGlamsterdam", TxAccessListAddressGasGlamsterdam, 600},
		{"TxAccessListStorageKeyGasGlamsterdam", TxAccessListStorageKeyGasGlamsterdam, 475},
		{"CallNewAccountGasGlamsterdam", CallNewAccountGasGlamsterdam, 6250},
		{"CreateGasGlamsterdam", CreateGasGlamsterdam, 8000},
		{"Create2GasGlamsterdam", Create2GasGlamsterdam, 8000},
		{"SstoreClearsScheduleRefundGlamsterdam", SstoreClearsScheduleRefundGlamsterdam, 3375},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestIsGlamsterdam(t *testing.T) {
	tests := []struct {
		name      string
		time      *big.Int
		checkTime uint64
		want      bool
	}{
		{"nil time means not active", nil, 100, false},
		{"before activation", big.NewInt(200), 100, false},
		{"at activation", big.NewInt(100), 100, true},
		{"after activation", big.NewInt(100), 200, true},
		{"zero activation", big.NewInt(0), 0, true},
		{"zero activation with later time", big.NewInt(0), 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ChainConfig{
				GlamsterdamTime: tt.time,
			}
			got := cfg.IsGlamsterdam(tt.checkTime)
			if got != tt.want {
				t.Errorf("IsGlamsterdam(%d) = %v, want %v", tt.checkTime, got, tt.want)
			}
		})
	}
}

func TestGlamsterdamForkInheritance(t *testing.T) {
	// When Glamsterdam is active, Fusaka (and all earlier forks) should also be active.
	cfg := &ChainConfig{
		ChainID:         big.NewInt(1),
		GlamsterdamTime: big.NewInt(100),
	}
	rules := cfg.RulesWithTimestamp(0, 100)
	if !rules.IsGlamsterdam {
		t.Fatal("expected IsGlamsterdam to be true at activation time")
	}
	if !rules.IsFusaka {
		t.Fatal("expected IsFusaka to be true when IsGlamsterdam is true (fork inheritance)")
	}
	if !rules.IsOsaka {
		t.Fatal("expected IsOsaka to be true when IsGlamsterdam is true (transitive fork inheritance)")
	}
	if !rules.IsPectra {
		t.Fatal("expected IsPectra to be true when IsGlamsterdam is true (transitive fork inheritance)")
	}
	if !rules.IsPrague {
		t.Fatal("expected IsPrague to be true when IsGlamsterdam is true (transitive fork inheritance)")
	}

	// When Glamsterdam is NOT active, the flag should be false.
	rulesBefore := cfg.RulesWithTimestamp(0, 50)
	if rulesBefore.IsGlamsterdam {
		t.Fatal("expected IsGlamsterdam to be false before activation time")
	}
}
