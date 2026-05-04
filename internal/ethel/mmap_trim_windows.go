// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mmap_trim_windows.go — periodic Windows working-set trim.
//
// Even with MDBX WriteMap disabled, file-backed mmap pages on Windows
// accumulate in the process working set and the kernel only reclaims
// them under HIGH pressure (Free < ~1 GB), by which point performance
// has already collapsed. EmptyWorkingSet asks the OS to remove pages
// from this process's WS proactively. The pages stay in system standby /
// file cache, so re-faulting is fast (no disk I/O).
//
// Effect on long-running forward replay:
//   - WS plateaus at the configured LRU + a small mmap residency
//   - OS Free RAM stays > 10 GB throughout
//   - Performance dip on the trim itself is < 1 ms (just rebuilds WS
//     entry list); next state read incurs a soft fault (microseconds)

//go:build windows

package ethel

import (
	"syscall"
	"sync/atomic"

	"github.com/n42blockchain/N42/log"
)

var (
	psapi               = syscall.NewLazyDLL("psapi.dll")
	procEmptyWorkingSet = psapi.NewProc("EmptyWorkingSet")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetCurProc      = kernel32.NewProc("GetCurrentProcess")

	// trimCount counts successful trims for diagnostics.
	trimCount atomic.Uint64
)

// TrimMemoryWorkingSet asks Windows to drop this process's working
// set down to the bare minimum. Pages stay in the standby cache —
// re-fault on next access is microsecond-level (no disk I/O), so
// the operation is cheap and effective.
//
// Safe to call from any goroutine. Returns nil error on Linux/macOS
// builds (see mmap_trim_unix.go).
func TrimMemoryWorkingSet() error {
	h, _, _ := procGetCurProc.Call()
	r, _, errno := procEmptyWorkingSet.Call(h)
	if r == 0 {
		log.Warn("EmptyWorkingSet failed", "err", errno)
		return errno
	}
	trimCount.Add(1)
	return nil
}

// TrimCount returns the cumulative number of successful trim calls.
func TrimCount() uint64 { return trimCount.Load() }
