//go:build !windows

/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package mmap

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const MaxMapSize = 0xFFFFFFFFFFFF

// MmapRw memory maps a file with read-write permissions.
func MmapRw(f *os.File, size int) ([]byte, *[MaxMapSize]byte, error) {
	return mmapWithProt(f, size, syscall.PROT_READ|syscall.PROT_WRITE)
}

// Mmap memory maps a file with read-only permissions.
func Mmap(f *os.File, size int) ([]byte, *[MaxMapSize]byte, error) {
	return mmapWithProt(f, size, syscall.PROT_READ)
}

func mmapWithProt(f *os.File, size int, prot int) ([]byte, *[MaxMapSize]byte, error) {
	mmapHandle1, err := unix.Mmap(int(f.Fd()), 0, size, prot, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	if err := madvise(mmapHandle1, syscall.MADV_RANDOM); err != nil {
		return nil, nil, err
	}
	mmapHandle2 := (*[MaxMapSize]byte)(unsafe.Pointer(&mmapHandle1[0]))
	return mmapHandle1, mmapHandle2, nil
}

func MadviseSequential(mmapHandle1 []byte) error { return madvise(mmapHandle1, syscall.MADV_SEQUENTIAL) }
func MadviseNormal(mmapHandle1 []byte) error     { return madvise(mmapHandle1, syscall.MADV_NORMAL) }
func MadviseWillNeed(mmapHandle1 []byte) error   { return madvise(mmapHandle1, syscall.MADV_WILLNEED) }
func MadviseRandom(mmapHandle1 []byte) error     { return madvise(mmapHandle1, syscall.MADV_RANDOM) }

// madvise calls unix.Madvise and ignores ENOSYS (not implemented).
func madvise(b []byte, advice int) error {
	err := unix.Madvise(b, advice)
	if err != nil && !errors.Is(err, syscall.ENOSYS) {
		return fmt.Errorf("madvise: %w", err)
	}
	return nil
}

// Munmap unmaps a previously mapped region.
func Munmap(mmapHandle1 []byte, _ *[MaxMapSize]byte) error {
	if mmapHandle1 == nil {
		return nil
	}
	return unix.Munmap(mmapHandle1)
}
