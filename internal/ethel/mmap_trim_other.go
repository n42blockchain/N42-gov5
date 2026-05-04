// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mmap_trim_other.go — non-Windows stub. Linux/macOS kernels page out
// cold mmap pages aggressively under memory pressure on their own,
// so the per-platform mmap-trim helper becomes a no-op.

//go:build !windows

package ethel

func TrimMemoryWorkingSet() error { return nil }
func TrimCount() uint64           { return 0 }
