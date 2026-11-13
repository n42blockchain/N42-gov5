// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build linux

package rawdb

import (
	"os"

	"golang.org/x/sys/unix"
)

// frameLogSync makes the file's data durable.
func frameLogSync(f *os.File) error { return unix.Fdatasync(int(f.Fd())) }

// frameLogPreallocate reserves the segment's blocks without changing its
// size, so appends do not allocate. Best effort.
func frameLogPreallocate(f *os.File, size int64) {
	_ = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_KEEP_SIZE, 0, size)
}
