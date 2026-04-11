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
// No-op watcher stub for platforms without fsnotify support.
// Build tag covers Windows, iOS, linux/arm64 and any non-POSIX
// systems that lack a usable inotify/kqueue backend. Declares the
// same watcher type used by account_cache but with start/close/
// enabled all no-ops; enabled() returns false so the cache falls
// back to the time-based minReloadInterval polling path rather than
// relying on filesystem event notifications.

//go:build (darwin && !cgo) || ios || (linux && arm64) || windows || (!darwin && !freebsd && !linux && !netbsd && !solaris)
// +build darwin,!cgo ios linux,arm64 windows !darwin,!freebsd,!linux,!netbsd,!solaris

// This is the fallback implementation of directory watching.
// It is used on unsupported platforms.

package keystore

type watcher struct {
	running  bool
	runEnded bool
}

func newWatcher(*accountCache) *watcher { return new(watcher) }
func (*watcher) start()                 {}
func (*watcher) close()                 {}

// enabled returns false on systems not supported.
func (*watcher) enabled() bool { return false }
