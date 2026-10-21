// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !cgo || !amd64 || gofuzz

package secp256k1x8

// Supported is false: the kernel needs cgo on amd64.
var Supported = false

// Recover panics: the kernel is not built on this platform.
func Recover(msgs [][32]byte, sigs [][65]byte, pubs [][65]byte, handled []bool) {
	if len(msgs) == 0 {
		return
	}
	panic("secp256k1x8: not built on this platform")
}
