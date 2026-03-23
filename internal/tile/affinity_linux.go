// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build linux

package tile

import (
	"golang.org/x/sys/unix"
)

// SetCPUAffinity pins the current OS thread to the specified CPU core.
// Must be called after runtime.LockOSThread().
func SetCPUAffinity(core int) error {
	var mask unix.CPUSet
	mask.Zero()
	mask.Set(core)
	return unix.SchedSetaffinity(0, &mask)
}
