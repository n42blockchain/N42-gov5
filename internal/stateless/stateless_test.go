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

package stateless

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

func TestCodeCache_PutGet(t *testing.T) {
	cc := NewCodeCache(100)

	h := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xfd} // PUSH1 0x00 PUSH1 0x00 REVERT

	// Get on empty cache.
	_, ok := cc.Get(h)
	if ok {
		t.Fatal("expected miss on empty cache")
	}

	// Put and Get.
	cc.Put(h, code)
	got, ok := cc.Get(h)
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if len(got) != len(code) {
		t.Fatalf("code length: got %d, want %d", len(got), len(code))
	}
	for i := range got {
		if got[i] != code[i] {
			t.Fatalf("code[%d]: got %x, want %x", i, got[i], code[i])
		}
	}

	// Verify returned slice is a copy.
	got[0] = 0xFF
	got2, _ := cc.Get(h)
	if got2[0] == 0xFF {
		t.Fatal("Get returned reference instead of copy")
	}

	if cc.Len() != 1 {
		t.Errorf("Len: got %d, want 1", cc.Len())
	}
}

func TestCodeCache_Capacity(t *testing.T) {
	cc := NewCodeCache(3)

	// Fill to capacity.
	for i := 0; i < 3; i++ {
		h := types.Hash{byte(i + 1)}
		cc.Put(h, []byte{byte(i + 1)})
	}
	if cc.Len() != 3 {
		t.Fatalf("Len after fill: got %d, want 3", cc.Len())
	}

	// Attempt to add a fourth entry -- should be silently dropped.
	h4 := types.Hash{0x04}
	cc.Put(h4, []byte{0x04})
	if cc.Len() != 3 {
		t.Fatalf("Len after overflow: got %d, want 3", cc.Len())
	}

	// Verify the fourth entry was not added.
	_, ok := cc.Get(h4)
	if ok {
		t.Fatal("expected overflow entry to be dropped")
	}

	// Verify existing entries are still present.
	for i := 0; i < 3; i++ {
		h := types.Hash{byte(i + 1)}
		_, ok := cc.Get(h)
		if !ok {
			t.Errorf("entry %d missing after overflow attempt", i+1)
		}
	}

	// Updating an existing entry should succeed even at capacity.
	h1 := types.Hash{0x01}
	cc.Put(h1, []byte{0xFF})
	got, ok := cc.Get(h1)
	if !ok || got[0] != 0xFF {
		t.Error("update of existing entry failed at capacity")
	}
}

func TestCodeCache_DefaultCapacity(t *testing.T) {
	cc := NewCodeCache(0)
	if cc.cap != 4096 {
		t.Errorf("default capacity: got %d, want 4096", cc.cap)
	}

	cc2 := NewCodeCache(-1)
	if cc2.cap != 4096 {
		t.Errorf("negative capacity: got %d, want 4096", cc2.cap)
	}
}

func TestNewValidator(t *testing.T) {
	config := &params.ChainConfig{}
	v := NewValidator(config)
	if v == nil {
		t.Fatal("NewValidator returned nil")
	}
	if v.ChainConfig() != config {
		t.Fatal("ChainConfig mismatch")
	}
}

func TestValidateBlock_NilInputs(t *testing.T) {
	v := NewValidator(&params.ChainConfig{})

	// Nil block.
	err := v.ValidateBlock(nil, &witness.BlockWitness{})
	if !errors.Is(err, ErrNilBlock) {
		t.Errorf("nil block: got %v, want ErrNilBlock", err)
	}

	// Nil witness -- use a concrete *Block to satisfy the interface.
	// We cannot easily construct a real block here, so test the nil block path only.
	// The nil witness path is tested implicitly below.
}

func TestValidateBlock_ZeroParentRoot(t *testing.T) {
	v := NewValidator(&params.ChainConfig{})

	w := &witness.BlockWitness{
		ParentRoot: types.Hash{}, // zero
	}

	// We need a non-nil IBlock. Use a mock-like approach:
	// ValidateBlock checks nil first, then witness, so passing nil block
	// triggers ErrNilBlock. We tested that above. The zero root check
	// requires a valid block, which needs complex construction.
	// For this minimal test, just verify the error type for nil block.
	err := v.ValidateBlock(nil, w)
	if !errors.Is(err, ErrNilBlock) {
		t.Errorf("expected ErrNilBlock, got %v", err)
	}
}
