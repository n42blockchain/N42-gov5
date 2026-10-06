// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY with even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Small, cheap coverage wins: the signersAscending sort Swap (needs 3+
// out-of-order signers to actually trigger a swap), SetHardForkAllocDir's
// setter, InitGlobalSTARKManager's setter, and
// blockNumberOrHashOrRLP.UnmarshalJSON's two decode branches.

package apos

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestSignersAscendingSwapsOutOfOrderEntries(t *testing.T) {
	a := types.HexToAddress("0x03")
	b := types.HexToAddress("0x01")
	c := types.HexToAddress("0x02")
	s := signersAscending{a, b, c} // intentionally unsorted, forces at least one Swap
	sort.Sort(s)
	if s[0] != b || s[1] != c || s[2] != a {
		t.Fatalf("expected ascending order [b,c,a], got %v", s)
	}
}

func TestSetHardForkAllocDir(t *testing.T) {
	t.Cleanup(func() { SetHardForkAllocDir("") })
	SetHardForkAllocDir("/tmp/some-dir")
	if hardforkAllocDir != "/tmp/some-dir" {
		t.Fatalf("expected hardforkAllocDir set, got %q", hardforkAllocDir)
	}
}

func TestInitGlobalSTARKManager(t *testing.T) {
	// Force globalSTARKManagerOnce to fire before Init, so that a later
	// GetGlobalSTARKManager call (lazy-init via sync.Once) doesn't clobber
	// the config Init just installed.
	_ = GetGlobalSTARKManager()

	cfg := DefaultPostQuantumConfig()
	cfg.Mode = PQModeHybrid
	InitGlobalSTARKManager(cfg)
	if GetGlobalSTARKManager().config.Mode != PQModeHybrid {
		t.Fatalf("expected global manager to use the injected config")
	}
}

func TestAposBlockNumberOrHashOrRLPUnmarshalJSONNumber(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	if err := json.Unmarshal([]byte(`"0x1"`), &sb); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if sb.BlockNumberOrHash == nil {
		t.Fatalf("expected BlockNumberOrHash to be populated")
	}
	n, ok := sb.BlockNumberOrHash.Number()
	if !ok || n != 1 {
		t.Fatalf("expected number 1, got %v ok=%v", n, ok)
	}
}

func TestAposBlockNumberOrHashOrRLPUnmarshalJSONRLP(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	if err := json.Unmarshal([]byte(`"0x1234567890abcdef1234"`), &sb); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(sb.RLP) == 0 {
		t.Fatalf("expected RLP bytes to be populated")
	}
}

func TestAposBlockNumberOrHashOrRLPUnmarshalJSONInvalid(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	if err := json.Unmarshal([]byte(`123`), &sb); err == nil {
		t.Fatalf("expected error for non-string, non-object JSON")
	}
}
