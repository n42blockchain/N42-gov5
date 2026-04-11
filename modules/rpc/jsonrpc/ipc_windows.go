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
// Windows named-pipe IPC transport backed by npipe.
// ipcListen delegates to npipe.Listen on the configured endpoint
// string. newIPCConnection honors the caller ctx.Deadline, falling
// back to defaultPipeDialTimeout (2s) and dialing the named pipe to
// produce a net.Conn suitable for wrapping with NewCodec.

//go:build windows
// +build windows

package jsonrpc

import (
	"context"
	"net"
	"time"

	"gopkg.in/natefinch/npipe.v2"
)

const defaultPipeDialTimeout = 2 * time.Second

func ipcListen(endpoint string) (net.Listener, error) {
	return npipe.Listen(endpoint)
}

func newIPCConnection(ctx context.Context, endpoint string) (net.Conn, error) {
	timeout := defaultPipeDialTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = deadline.Sub(time.Now())
		if timeout < 0 {
			timeout = 0
		}
	}
	return npipe.DialTimeout(endpoint, timeout)
}
