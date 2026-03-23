// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !linux

package tile

// SetCPUAffinity is a no-op on non-Linux platforms.
func SetCPUAffinity(core int) error {
	return nil
}
