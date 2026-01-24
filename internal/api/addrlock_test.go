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

package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestAddrLockerLockUnlock(t *testing.T) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Lock and unlock should not panic
	locker.LockAddr(addr)
	locker.UnlockAddr(addr)
}

func TestAddrLockerMultipleAddresses(t *testing.T) {
	locker := &AddrLocker{}
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// Lock both addresses
	locker.LockAddr(addr1)
	locker.LockAddr(addr2)

	// Unlock in reverse order
	locker.UnlockAddr(addr2)
	locker.UnlockAddr(addr1)
}

func TestAddrLockerConcurrentAccess(t *testing.T) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	var counter int32
	var wg sync.WaitGroup

	// Start multiple goroutines that try to lock the same address
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locker.LockAddr(addr)
			// Critical section
			atomic.AddInt32(&counter, 1)
			time.Sleep(time.Millisecond)
			locker.UnlockAddr(addr)
		}()
	}

	wg.Wait()

	if atomic.LoadInt32(&counter) != 10 {
		t.Errorf("Counter = %d, want 10", counter)
	}
}

func TestAddrLockerDifferentAddressesConcurrent(t *testing.T) {
	locker := &AddrLocker{}
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	var wg sync.WaitGroup
	var counter1, counter2 int32

	// Goroutines for addr1
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locker.LockAddr(addr1)
			atomic.AddInt32(&counter1, 1)
			time.Sleep(time.Millisecond)
			locker.UnlockAddr(addr1)
		}()
	}

	// Goroutines for addr2 - should not block each other
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locker.LockAddr(addr2)
			atomic.AddInt32(&counter2, 1)
			time.Sleep(time.Millisecond)
			locker.UnlockAddr(addr2)
		}()
	}

	wg.Wait()

	if atomic.LoadInt32(&counter1) != 5 {
		t.Errorf("Counter1 = %d, want 5", counter1)
	}
	if atomic.LoadInt32(&counter2) != 5 {
		t.Errorf("Counter2 = %d, want 5", counter2)
	}
}

func TestAddrLockerLockReturnsConsistentMutex(t *testing.T) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Get lock twice for same address - should return same mutex
	mu1 := locker.lock(addr)
	mu2 := locker.lock(addr)

	if mu1 != mu2 {
		t.Error("lock() should return the same mutex for the same address")
	}
}

func TestAddrLockerZeroAddress(t *testing.T) {
	locker := &AddrLocker{}
	zeroAddr := types.Address{}

	// Should work with zero address
	locker.LockAddr(zeroAddr)
	locker.UnlockAddr(zeroAddr)
}

func TestAddrLockerInitializesMapOnFirstUse(t *testing.T) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// locks map should be nil initially
	if locker.locks != nil {
		t.Error("locks should be nil before first use")
	}

	// After lock, map should be initialized
	locker.LockAddr(addr)
	locker.UnlockAddr(addr)

	if locker.locks == nil {
		t.Error("locks should be initialized after use")
	}
}

func BenchmarkAddrLockerConcurrentNew(b *testing.B) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			locker.LockAddr(addr)
			locker.UnlockAddr(addr)
		}
	})
}
