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
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Mock Types for ForkChoice Testing
// =============================================================================

// mockChainReader implements ChainReader for testing
type mockChainReader struct {
	tds map[types.Hash]*uint256.Int
}

func newMockChainReader() *mockChainReader {
	return &mockChainReader{
		tds: make(map[types.Hash]*uint256.Int),
	}
}

func (m *mockChainReader) GetTd(hash types.Hash, _ *uint256.Int) *uint256.Int {
	if td, ok := m.tds[hash]; ok {
		return td
	}
	return uint256.NewInt(0)
}

func (m *mockChainReader) setTd(hash types.Hash, td *uint256.Int) {
	m.tds[hash] = td
}

// =============================================================================
// ForkChoice Tests
// =============================================================================

func TestNewForkChoice(t *testing.T) {
	chain := newMockChainReader()
	fc := NewForkChoice(chain, nil)

	if fc == nil {
		t.Fatal("NewForkChoice should not return nil")
	}
	if fc.chain != chain {
		t.Error("ForkChoice should store the chain reader")
	}
	if fc.rand == nil {
		t.Error("ForkChoice should have initialized random generator")
	}

	t.Logf("✓ NewForkChoice creates valid instance")
}

func TestNewForkChoiceWithPreserve(t *testing.T) {
	chain := newMockChainReader()
	preserve := func(header block.IHeader) bool {
		return true
	}

	fc := NewForkChoice(chain, preserve)

	if fc.preserve == nil {
		t.Error("ForkChoice should store preserve function")
	}

	t.Logf("✓ NewForkChoice with preserve function works")
}

// =============================================================================
// ChainReader Interface Tests
// =============================================================================

func TestChainReaderInterface(t *testing.T) {
	var _ ChainReader = newMockChainReader()
	t.Logf("✓ ChainReader interface is correctly defined")
}

func TestMockChainReader_GetTd(t *testing.T) {
	chain := newMockChainReader()
	hash := types.Hash{0x01}
	td := uint256.NewInt(12345)

	chain.setTd(hash, td)
	result := chain.GetTd(hash, nil)

	if result.Cmp(td) != 0 {
		t.Errorf("GetTd = %v, want %v", result, td)
	}

	t.Logf("✓ mockChainReader GetTd works correctly")
}

func TestMockChainReader_GetTd_NotFound(t *testing.T) {
	chain := newMockChainReader()
	hash := types.Hash{0x99}

	result := chain.GetTd(hash, nil)

	if result == nil {
		t.Error("GetTd should return non-nil for unknown hash")
	}
	if result.Cmp(uint256.NewInt(0)) != 0 {
		t.Errorf("GetTd should return 0 for unknown hash, got %v", result)
	}

	t.Logf("✓ mockChainReader GetTd returns 0 for unknown hash")
}

func TestTDComparison(t *testing.T) {
	tests := []struct {
		name     string
		localTD  uint64
		externTD uint64
		wantCmp  int // extern.Cmp(local): 1 if extern > local, -1 if extern < local, 0 if equal
	}{
		{"extern_higher", 100, 200, 1}, // 200 > 100
		{"local_higher", 200, 100, -1}, // 100 < 200
		{"equal", 100, 100, 0},
		{"zero", 0, 0, 0},
		{"large_values", 999999, 1000000, 1}, // 1000000 > 999999
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := uint256.NewInt(tt.localTD)
			extern := uint256.NewInt(tt.externTD)
			cmp := extern.Cmp(local)
			if cmp != tt.wantCmp {
				t.Errorf("Cmp() = %d, want %d", cmp, tt.wantCmp)
			}
		})
	}

	t.Logf("✓ Total difficulty comparison works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkNewForkChoice(b *testing.B) {
	chain := newMockChainReader()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NewForkChoice(chain, nil)
	}
}

func BenchmarkGetTd(b *testing.B) {
	chain := newMockChainReader()
	hash := types.Hash{0x01}
	chain.setTd(hash, uint256.NewInt(12345))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chain.GetTd(hash, nil)
	}
}

func BenchmarkTDComparison(b *testing.B) {
	local := uint256.NewInt(100)
	extern := uint256.NewInt(200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extern.Cmp(local)
	}
}

func BenchmarkUint256Clone(b *testing.B) {
	value := uint256.NewInt(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		value.Clone()
	}
}
