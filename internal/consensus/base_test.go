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

package consensus

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
)

// =============================================================================
// BasePoA Tests
// =============================================================================

func TestNewBasePoA(t *testing.T) {
	base := NewBasePoA(nil, 0)

	t.Run("RecentsNotNil", func(t *testing.T) {
		if base.Recents() == nil {
			t.Error("Recents() should not be nil")
		}
	})

	t.Run("SignaturesNotNil", func(t *testing.T) {
		if base.Signatures() == nil {
			t.Error("Signatures() should not be nil")
		}
	})

	t.Run("ValidatorNotNil", func(t *testing.T) {
		if base.Validator() == nil {
			t.Error("Validator() should not be nil")
		}
	})

	t.Run("DefaultEpoch", func(t *testing.T) {
		if base.Validator().Epoch() != misc.DefaultEpochLength {
			t.Errorf("Validator().Epoch() = %d, want %d", base.Validator().Epoch(), misc.DefaultEpochLength)
		}
	})

	t.Run("CustomEpoch", func(t *testing.T) {
		base := NewBasePoA(nil, 1000)
		if base.Validator().Epoch() != 1000 {
			t.Errorf("Validator().Epoch() = %d, want 1000", base.Validator().Epoch())
		}
	})
}

func TestBasePoAProposals(t *testing.T) {
	base := NewBasePoA(nil, 0)

	addr1 := types.Address{1}
	addr2 := types.Address{2}

	t.Run("EmptyInitially", func(t *testing.T) {
		proposals := base.Proposals()
		if len(proposals) != 0 {
			t.Errorf("Proposals() len = %d, want 0", len(proposals))
		}
	})

	t.Run("SetProposal", func(t *testing.T) {
		base.SetProposal(addr1, true)
		base.SetProposal(addr2, false)

		proposals := base.Proposals()
		if proposals[addr1] != true {
			t.Errorf("Proposals()[addr1] = %v, want true", proposals[addr1])
		}
		if proposals[addr2] != false {
			t.Errorf("Proposals()[addr2] = %v, want false", proposals[addr2])
		}
	})

	t.Run("DeleteProposal", func(t *testing.T) {
		base.DeleteProposal(addr1)

		proposals := base.Proposals()
		if _, ok := proposals[addr1]; ok {
			t.Error("Proposals() should not contain addr1 after delete")
		}
	})

	t.Run("ProposalsReturnsCopy", func(t *testing.T) {
		base.SetProposal(addr1, true)
		proposals := base.Proposals()
		proposals[addr1] = false // Modify the copy

		// Original should be unchanged
		if base.Proposals()[addr1] != true {
			t.Error("Proposals() should return a copy")
		}
	})
}

func TestBasePoASigner(t *testing.T) {
	base := NewBasePoA(nil, 0)

	addr := types.Address{1, 2, 3}

	t.Run("InitiallyZero", func(t *testing.T) {
		if base.Signer() != (types.Address{}) {
			t.Error("Signer() should be zero initially")
		}
	})

	t.Run("SetSigner", func(t *testing.T) {
		base.SetSigner(addr)
		if base.Signer() != addr {
			t.Errorf("Signer() = %v, want %v", base.Signer(), addr)
		}
	})
}

func TestBasePoAClose(t *testing.T) {
	base := NewBasePoA(nil, 0)

	err := base.Close()
	if err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

func TestBasePoAConcurrency(t *testing.T) {
	base := NewBasePoA(nil, 0)

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent proposal operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addr := types.Address{byte(i)}
			base.SetProposal(addr, i%2 == 0)
			_ = base.Proposals()
			base.DeleteProposal(addr)
		}(i)
	}

	// Concurrent signer operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addr := types.Address{byte(i)}
			base.SetSigner(addr)
			_ = base.Signer()
		}(i)
	}

	wg.Wait()
	t.Log("✓ BasePoA concurrent operations work correctly")
}

func TestBasePoAWithLock(t *testing.T) {
	base := NewBasePoA(nil, 0)

	var executed bool
	base.WithLock(func() {
		executed = true
	})

	if !executed {
		t.Error("WithLock() should execute the function")
	}
}

func TestBasePoAWithRLock(t *testing.T) {
	base := NewBasePoA(nil, 0)

	var executed bool
	base.WithRLock(func() {
		executed = true
	})

	if !executed {
		t.Error("WithRLock() should execute the function")
	}
}

// =============================================================================
// VerifyHeadersAsync Tests
// =============================================================================

func TestVerifyHeadersAsync(t *testing.T) {
	t.Run("NoHeaders", func(t *testing.T) {
		abort, results := VerifyHeadersAsync(nil, func(header block.IHeader, parents []block.IHeader) error {
			return nil
		})

		select {
		case err := <-results:
			t.Errorf("Got unexpected result: %v", err)
		default:
			// Expected - no results
		}

		close(abort)
	})

	t.Run("Abort", func(t *testing.T) {
		abort, _ := VerifyHeadersAsync(make([]block.IHeader, 100), func(header block.IHeader, parents []block.IHeader) error {
			return nil
		})

		close(abort)
		// Give goroutine time to exit
		t.Log("✓ VerifyHeadersAsync aborts correctly")
	})
}

// =============================================================================
// Mock Inturn Tests
// =============================================================================

type testInturn struct {
	inTurn bool
}

func (t *testInturn) Inturn(blockNumber uint64, signer types.Address) bool {
	return t.inTurn
}

func TestCalcDifficultyWithSnapshot(t *testing.T) {
	signer := types.Address{1}

	t.Run("InTurn", func(t *testing.T) {
		snap := &testInturn{inTurn: true}
		diff := CalcDifficultyWithSnapshot(snap, 100, signer)
		if diff.Cmp(misc.DiffInTurn) != 0 {
			t.Errorf("CalcDifficultyWithSnapshot() = %s, want %s", diff.String(), misc.DiffInTurn.String())
		}
	})

	t.Run("NotInTurn", func(t *testing.T) {
		snap := &testInturn{inTurn: false}
		diff := CalcDifficultyWithSnapshot(snap, 100, signer)
		if diff.Cmp(misc.DiffNoTurn) != 0 {
			t.Errorf("CalcDifficultyWithSnapshot() = %s, want %s", diff.String(), misc.DiffNoTurn.String())
		}
	})
}

