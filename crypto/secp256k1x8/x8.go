// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build cgo && amd64 && !gofuzz

// Package secp256k1x8 recovers secp256k1 public keys eight at a time with
// AVX-512 IFMA (see recover_x8.c). Lanes it does not handle are reported,
// never guessed: callers recover those with libsecp256k1.
package secp256k1x8

/*
#cgo CFLAGS: -O3
#cgo noescape n42_x8_recover
#cgo nocallback n42_x8_recover
#include "recover_x8.h"
*/
import "C"

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// Supported reports whether this CPU (and OS) can run the kernel.
var Supported = cpu.X86.HasAVX512F && cpu.X86.HasAVX512IFMA && cpu.X86.HasAVX512DQ &&
	cpu.X86.HasAVX512VL && cpu.X86.HasAVX512BW && cpu.X86.HasBMI2

var initOnce sync.Once

// Recover recovers the public keys of len(msgs) signatures: for each i with
// handled[i] set, pubs[i] is the 65-byte uncompressed key libsecp256k1
// would return for (msgs[i], sigs[i]). An unset handled[i] means the lane
// was not recovered here (invalid or unusual input) and pubs[i] is
// untouched. sigs, pubs and handled must be at least as long as msgs. It
// panics when the CPU is not Supported.
func Recover(msgs [][32]byte, sigs [][65]byte, pubs [][65]byte, handled []bool) {
	n := len(msgs)
	if n == 0 {
		return
	}
	if !Supported {
		panic("secp256k1x8: AVX-512 IFMA not supported")
	}
	_ = sigs[n-1]
	_ = pubs[n-1]
	_ = handled[n-1]
	initOnce.Do(func() { C.n42_x8_init() })
	C.n42_x8_recover(C.size_t(n), (*C.uint8_t)(unsafe.Pointer(&msgs[0][0])), (*C.uint8_t)(unsafe.Pointer(&sigs[0][0])),
		(*C.uint8_t)(unsafe.Pointer(&pubs[0][0])), (*C.uint8_t)(unsafe.Pointer(&handled[0])))
}
