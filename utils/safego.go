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

package utils

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/log"
)

// SafeGo launches a goroutine with automatic panic recovery.
// On panic, it logs the error with the provided label and full stack trace,
// then returns normally instead of crashing the process.
func SafeGo(label string, f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Error("panic in goroutine, recovered", "label", label, "panic", r, "stack", string(buf[:n]))
			}
		}()
		f()
	}()
}

// SafeGoWithWG is like SafeGo but decrements a WaitGroup when the goroutine
// exits. The caller must call wg.Add(1) before invoking this function.
func SafeGoWithWG(label string, wg *sync.WaitGroup, f func()) {
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Error("panic in goroutine, recovered", "label", label, "panic", r, "stack", string(buf[:n]))
			}
		}()
		f()
	}()
}

// SafeGoErr launches a goroutine with panic recovery that converts panics
// to errors sent on the returned channel. Useful for errgroup-like patterns
// where the caller needs to observe errors from spawned goroutines.
func SafeGoErr(label string, f func() error) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Error("panic in goroutine, recovered", "label", label, "panic", r, "stack", string(buf[:n]))
				ch <- fmt.Errorf("panic in %s: %v", label, r)
			}
		}()
		ch <- f()
	}()
	return ch
}
