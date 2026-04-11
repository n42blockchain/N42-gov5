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
// Non-Windows stub for packet-too-big detection.
// Build-tagged !windows. isPacketTooBig always returns false, since
// the WSAEMSGSIZE condition is a Windows-only IPC surface and Unix
// sockets surface fragmentation through different error codes.

//go:build !windows
// +build !windows

package jsonrpc

func isPacketTooBig(err error) bool {
	return false
}
