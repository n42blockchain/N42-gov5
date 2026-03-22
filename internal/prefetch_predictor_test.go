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
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestPrefetchPredictor_RecordAndPredict(t *testing.T) {
	pp := NewPrefetchPredictor(64)
	contract := types.Address{0x01}

	slotA := types.Hash{0x0A}
	slotB := types.Hash{0x0B}
	slotC := types.Hash{0x0C}

	// Record accesses: A=3, B=1, C=2
	pp.RecordSlotAccess(contract, slotA)
	pp.RecordSlotAccess(contract, slotA)
	pp.RecordSlotAccess(contract, slotA)
	pp.RecordSlotAccess(contract, slotB)
	pp.RecordSlotAccess(contract, slotC)
	pp.RecordSlotAccess(contract, slotC)

	predicted := pp.PredictSlots(contract, 3)
	if len(predicted) != 3 {
		t.Fatalf("expected 3 predicted slots, got %d", len(predicted))
	}

	// First should be slotA (highest count).
	if predicted[0] != slotA {
		t.Errorf("expected first slot to be A, got %x", predicted[0])
	}
	// Second should be slotC (count=2).
	if predicted[1] != slotC {
		t.Errorf("expected second slot to be C, got %x", predicted[1])
	}
	// Third should be slotB (count=1).
	if predicted[2] != slotB {
		t.Errorf("expected third slot to be B, got %x", predicted[2])
	}
}

func TestPrefetchPredictor_MaxSlotsCapped(t *testing.T) {
	maxSlots := 3
	pp := NewPrefetchPredictor(maxSlots)
	contract := types.Address{0x02}

	// Fill up to maxSlots.
	for i := 0; i < maxSlots; i++ {
		slot := types.Hash{byte(i)}
		pp.RecordSlotAccess(contract, slot)
	}

	// Record one more slot, should evict the lowest.
	newSlot := types.Hash{0xFF}
	pp.RecordSlotAccess(contract, newSlot)

	contracts, slots := pp.Stats()
	if contracts != 1 {
		t.Errorf("expected 1 contract, got %d", contracts)
	}
	if slots != maxSlots {
		t.Errorf("expected %d slots, got %d", maxSlots, slots)
	}

	// The new slot should be present.
	predicted := pp.PredictSlots(contract, maxSlots+1)
	found := false
	for _, s := range predicted {
		if s == newSlot {
			found = true
			break
		}
	}
	if !found {
		t.Error("new slot should be present after eviction")
	}
}

func TestPrefetchPredictor_Decay(t *testing.T) {
	pp := NewPrefetchPredictor(64)
	contract := types.Address{0x03}

	slot := types.Hash{0x01}

	// Record 10 accesses.
	for i := 0; i < 10; i++ {
		pp.RecordSlotAccess(contract, slot)
	}

	// Decay by 0.5 -> count should be 5.
	pp.Decay(0.5)

	predicted := pp.PredictSlots(contract, 1)
	if len(predicted) != 1 {
		t.Fatalf("expected 1 slot after decay, got %d", len(predicted))
	}

	// Verify count is halved by checking stats still report the slot.
	_, slots := pp.Stats()
	if slots != 1 {
		t.Errorf("expected 1 slot, got %d", slots)
	}

	// Decay again with factor that should zero out count=5 -> 5*0.1=0.
	pp.Decay(0.1)

	_, slots = pp.Stats()
	if slots != 0 {
		t.Errorf("expected 0 slots after aggressive decay, got %d", slots)
	}
}

func TestPrefetchPredictor_UnknownContract(t *testing.T) {
	pp := NewPrefetchPredictor(64)
	unknown := types.Address{0xFF}

	predicted := pp.PredictSlots(unknown, 10)
	if predicted != nil {
		t.Errorf("expected nil for unknown contract, got %v", predicted)
	}
}

func TestPrefetchPredictor_Concurrent(t *testing.T) {
	pp := NewPrefetchPredictor(64)
	contract := types.Address{0x04}

	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				slot := types.Hash{byte(id), byte(i % 10)}
				pp.RecordSlotAccess(contract, slot)
				pp.PredictSlots(contract, 5)
			}
		}(g)
	}
	wg.Wait()

	// Just verify no panic and stats are consistent.
	contracts, slots := pp.Stats()
	if contracts != 1 {
		t.Errorf("expected 1 contract, got %d", contracts)
	}
	if slots == 0 {
		t.Error("expected non-zero slots after concurrent access")
	}
}
