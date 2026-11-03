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

package common

import (
	"errors"
	"testing"
)

// EIP-8037: the block meters execution and state gas separately; its gas
// used is the larger of the two cumulative figures, and each transaction's
// worst case must fit what is left of each dimension.
func TestGasPoolAmsterdamTwoDimensions(t *testing.T) {
	gp := NewGasPool(100)

	if err := gp.CheckGasAmsterdam(100, 100); err != nil {
		t.Fatalf("empty block rejects a full-limit tx: %v", err)
	}
	if err := gp.ChargeGasAmsterdam(60, 30); err != nil {
		t.Fatalf("charge (60, 30): %v", err)
	}
	if got := gp.UsedAmsterdam(); got != 60 {
		t.Fatalf("used = %d, want 60", got)
	}
	if got := gp.Gas(); got != 40 {
		t.Fatalf("execution gas left = %d, want 40", got)
	}
	// 41 execution gas no longer fits; state still has room for 70.
	if err := gp.CheckGasAmsterdam(41, 10); !errors.Is(err, ErrGasLimitReached) {
		t.Fatalf("execution over the limit: err = %v, want ErrGasLimitReached", err)
	}
	if err := gp.CheckGasAmsterdam(40, 71); !errors.Is(err, ErrGasLimitReached) {
		t.Fatalf("state over the limit: err = %v, want ErrGasLimitReached", err)
	}
	if err := gp.CheckGasAmsterdam(40, 70); err != nil {
		t.Fatalf("tx at both limits: %v", err)
	}
	// A charge that would push either dimension past the limit is refused
	// and leaves the counters alone.
	if err := gp.ChargeGasAmsterdam(30, 80); !errors.Is(err, ErrGasLimitReached) {
		t.Fatalf("overflowing charge: err = %v, want ErrGasLimitReached", err)
	}
	// State gas overtakes execution gas: the block's gas used follows it.
	if err := gp.ChargeGasAmsterdam(10, 60); err != nil {
		t.Fatalf("charge (10, 60): %v", err)
	}
	if got := gp.UsedAmsterdam(); got != 90 {
		t.Fatalf("used = %d, want max(70, 90) = 90", got)
	}
}
