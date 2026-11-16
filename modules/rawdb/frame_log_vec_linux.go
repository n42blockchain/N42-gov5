// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build linux

package rawdb

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// frameLogIovMax is the kernel's limit on the slices of one pwritev.
const frameLogIovMax = 1024

// frameLogWriteVec writes iov at off with pwritev, in chunks of at most
// frameLogIovMax slices, resuming after a short write.
func frameLogWriteVec(f *os.File, iov [][]byte, off int64) error {
	fd := int(f.Fd())
	cur := make([][]byte, 0, frameLogIovMax)
	for len(iov) > 0 {
		cur = cur[:0]
		for _, b := range iov {
			if len(cur) == frameLogIovMax {
				break
			}
			cur = append(cur, b)
		}
		n, err := unix.Pwritev(fd, cur, off)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &os.PathError{Op: "pwritev", Path: f.Name(), Err: err}
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		off += int64(n)
		// Drop what was written; a partly written slice is resumed.
		for n > 0 {
			if n >= len(iov[0]) {
				n -= len(iov[0])
				iov = iov[1:]
				continue
			}
			iov = append([][]byte{iov[0][n:]}, iov[1:]...)
			n = 0
		}
	}
	return nil
}
