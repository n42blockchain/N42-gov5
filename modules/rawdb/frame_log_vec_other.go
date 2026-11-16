// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !linux

package rawdb

import "os"

// frameLogWriteVec writes iov at off.
func frameLogWriteVec(f *os.File, iov [][]byte, off int64) error {
	return frameLogWriteVecAt(f, iov, off)
}
