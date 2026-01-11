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

package eth69

import "errors"

var (
	// ErrUnsupportedProtocolVersion is returned when a peer announces an unsupported protocol version.
	ErrUnsupportedProtocolVersion = errors.New("unsupported protocol version")

	// ErrNetworkIDMismatch is returned when a peer's network ID doesn't match ours.
	ErrNetworkIDMismatch = errors.New("network ID mismatch")

	// ErrGenesisHashMismatch is returned when a peer's genesis hash doesn't match ours.
	ErrGenesisHashMismatch = errors.New("genesis hash mismatch")

	// ErrInvalidBlockRange is returned when a peer announces an invalid block range.
	ErrInvalidBlockRange = errors.New("invalid block range")

	// ErrInvalidStatus is returned when a status message is malformed.
	ErrInvalidStatus = errors.New("invalid status message")

	// ErrInvalidBlockRangeUpdate is returned when a BlockRangeUpdate message is malformed.
	ErrInvalidBlockRangeUpdate = errors.New("invalid block range update")

	// ErrPeerNotFound is returned when trying to access a peer that doesn't exist.
	ErrPeerNotFound = errors.New("peer not found")

	// ErrNoBlocksAvailable is returned when no peers have the requested blocks.
	ErrNoBlocksAvailable = errors.New("no peers have the requested blocks")
)
