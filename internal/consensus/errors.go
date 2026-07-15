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
//
// Consensus-package-wide error sentinels for header validation.
// ErrUnknownAncestor, ErrUnknownAncestorTD and ErrPrunedAncestor cover
// missing or pruned parent state. ErrFutureBlock guards against
// timestamps ahead of local wall time. ErrInvalidNumber and
// ErrNotEnoughSign flag number and BLS quorum violations respectively.

package consensus

import (
	"context"
	"errors"
)

var (
	// ErrUnknownAncestor is returned when validating a block requires an ancestor
	// that is unknown.
	ErrUnknownAncestor = errors.New("unknown ancestor")

	// ErrUnknownAncestorTD is returned when validating a block requires an ancestor
	// whose total difficulty is unknown.
	ErrUnknownAncestorTD = errors.New("unknown ancestor TD")

	// ErrPrunedAncestor is returned when validating a block requires an ancestor
	// that is known, but the state of which is not available.
	ErrPrunedAncestor = errors.New("pruned ancestor")

	// ErrFutureBlock is returned when a block's timestamp is in the future according
	// to the current node.
	ErrFutureBlock = errors.New("block in the future")

	// ErrInvalidNumber is returned if a block's number doesn't equal its parent's
	// plus one.
	ErrInvalidNumber = errors.New("invalid block number")

	// ErrNotEnoughSign is returned when there are insufficient BLS signatures
	// to meet the consensus threshold.
	ErrNotEnoughSign = errors.New("not enough sign")

	// ErrExecutionInvalid marks a block that was executed against its parent
	// state and failed a deterministic execution or post-state consensus check.
	// Callers may safely cache the block as bad. Local availability and ordering
	// failures (unknown/pruned ancestors, future blocks, or unavailable reverts)
	// must never wrap this sentinel.
	ErrExecutionInvalid = errors.New("block execution invalid")

	// ErrInternal marks a transient/internal failure encountered while trying to
	// import a block — database or storage-engine I/O, an unavailable reader, or
	// a shutdown — as opposed to a deterministic execution/consensus violation.
	// A block failing with ErrInternal is NOT bad: it may import cleanly once the
	// transient condition clears. Wrap internal errors with this before they reach
	// the bad-block gate so they are never cached as bad.
	ErrInternal = errors.New("internal block import error")
)

// IsInternalError reports whether err is a transient/internal import failure
// rather than a deterministic execution or consensus violation, and therefore
// must NOT mark the block bad. This mirrors reth's is_validation_error() split:
// only genuine validation errors may be cached as bad; context cancellation,
// missing/pruned/future ancestors and anything explicitly wrapped as ErrInternal
// are retryable and bubble up instead.
func IsInternalError(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrInternal),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrUnknownAncestor),
		errors.Is(err, ErrUnknownAncestorTD),
		errors.Is(err, ErrPrunedAncestor),
		errors.Is(err, ErrFutureBlock):
		return true
	}
	return false
}
