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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitGroupOrTimeout(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, desc string) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", desc)
	}
}

func TestSafeGo_NoPanic(t *testing.T) {
	done := make(chan struct{})
	SafeGo("test-no-panic", func() {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goroutine did not run")
	}
}

func TestSafeGo_PanicRecovery(t *testing.T) {
	var after atomic.Bool
	done := make(chan struct{})

	SafeGo("test-panic", func() {
		defer func() {
			after.Store(true)
			close(done)
		}()
		panic("test panic")
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panicing goroutine did not finish")
	}
	if !after.Load() {
		t.Fatal("deferred cleanup did not run before panic recovery")
	}
}

func TestSafeGoWithWG_NoPanic(t *testing.T) {
	var wg sync.WaitGroup
	var done atomic.Bool
	wg.Add(1)
	SafeGoWithWG("test-wg", &wg, func() {
		done.Store(true)
	})
	waitGroupOrTimeout(t, &wg, time.Second, "SafeGoWithWG without panic")
	if !done.Load() {
		t.Fatal("goroutine did not run")
	}
}

func TestSafeGoWithWG_PanicRecovery(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	SafeGoWithWG("test-wg-panic", &wg, func() {
		panic("test wg panic")
	})

	waitGroupOrTimeout(t, &wg, time.Second, "SafeGoWithWG panic recovery")
}

func TestSafeGoErr_NoPanic(t *testing.T) {
	errCh := SafeGoErr("test-err", func() error {
		return nil
	})
	err := <-errCh
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeGoErr_Error(t *testing.T) {
	want := errors.New("test error")
	errCh := SafeGoErr("test-err-val", func() error {
		return want
	})
	err := <-errCh
	if err != want {
		t.Fatalf("want %v, got %v", want, err)
	}
}

func TestSafeGoErr_PanicBecomesError(t *testing.T) {
	errCh := SafeGoErr("test-err-panic", func() error {
		panic("boom")
	})
	err := <-errCh
	if err == nil {
		t.Fatal("expected error from panic")
	}
	if err.Error() != "panic in test-err-panic: boom" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSafeGo_MultipleConcurrent(t *testing.T) {
	var count atomic.Int32
	var wg sync.WaitGroup
	const n = 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		SafeGo("concurrent", func() {
			defer wg.Done()
			count.Add(1)
		})
	}
	waitGroupOrTimeout(t, &wg, 2*time.Second, "all concurrent SafeGo calls")
	if count.Load() != n {
		t.Fatalf("expected %d, got %d", n, count.Load())
	}
}
