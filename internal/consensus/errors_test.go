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
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsInternalError(t *testing.T) {
	internal := []error{
		ErrInternal,
		fmt.Errorf("read account: %w", ErrInternal),
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("import: %w", context.Canceled),
		ErrUnknownAncestor,
		ErrUnknownAncestorTD,
		ErrPrunedAncestor,
		ErrFutureBlock,
	}
	for _, err := range internal {
		if !IsInternalError(err) {
			t.Errorf("IsInternalError(%v) = false, want true (must NOT mark bad)", err)
		}
	}

	// Deterministic validation / execution violations MUST NOT be treated as
	// internal — they stay eligible to be cached as bad blocks.
	validation := []error{
		ErrExecutionInvalid,
		fmt.Errorf("%w: state root mismatch", ErrExecutionInvalid),
		ErrInvalidNumber,
		ErrNotEnoughSign,
		errors.New("invalid transaction signature"),
		errors.New("insufficient funds for gas"),
	}
	for _, err := range validation {
		if IsInternalError(err) {
			t.Errorf("IsInternalError(%v) = true, want false (validation error must stay markable bad)", err)
		}
	}

	if IsInternalError(nil) {
		t.Error("IsInternalError(nil) = true, want false")
	}
}
