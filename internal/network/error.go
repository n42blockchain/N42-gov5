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

package network

import (
	"errors"
	"fmt"
)

var (
	errBadMsgType        = errors.New("p2p: message type is invalid")
	errBadLengthEncoding = errors.New("p2p: remaining length field exceeded maximum of 4 bytes")
	errBadReturnCode     = errors.New("p2p: return code is invalid")
	errDataExceedsPacket = errors.New("p2p: data exceeds packet length")
	errMsgTooLong        = errors.New("p2p: message is too long")
	errPackageMsg        = errors.New("p2p: package message error")
	errPeerNotFound      = errors.New("p2p: peer not found")
)

type panicErr struct {
	err error
}

func (p panicErr) Error() string {
	return p.err.Error()
}

func raiseError(err error) {
	panic(panicErr{err})
}

func recoverError(existingErr error, recovered any) error {
	if recovered == nil {
		return existingErr
	}
	if pErr, ok := recovered.(panicErr); ok {
		return pErr.err
	}
	// Convert unexpected panics to errors instead of re-panicking,
	// which would crash the process without any log output.
	if err, ok := recovered.(error); ok {
		return fmt.Errorf("unexpected panic in network layer: %w", err)
	}
	return fmt.Errorf("unexpected panic in network layer: %v", recovered)
}
