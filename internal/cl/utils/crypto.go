// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Crypto unit for the utils package.
// Exports helpers such as Sha256 and OptimizedSha256NotThreadSafe.
// Miscellaneous consensus-layer utilities.

//go:build n42el

package utils

import (
	"crypto/sha256"
	"hash"
	"sync"
)

type HashFunc func(data []byte, extras ...[]byte) [32]byte

var hasherPool = sync.Pool{
	New: func() any {
		return sha256.New()
	},
}

// General purpose Sha256
func Sha256(data []byte, extras ...[]byte) [32]byte {
	h, ok := hasherPool.Get().(hash.Hash)
	if !ok {
		h = sha256.New()
	}
	defer hasherPool.Put(h)
	h.Reset()

	var b [32]byte

	h.Write(data)
	for _, extra := range extras {
		h.Write(extra)
	}
	h.Sum(b[:0])
	return b
}

// Optimized Sha256, avoid pool.put/pool.get, meant for intensive operations.
// this version is not thread safe
func OptimizedSha256NotThreadSafe() HashFunc {
	h := sha256.New()
	var b [32]byte
	return func(data []byte, extras ...[]byte) [32]byte {
		h.Reset()
		h.Write(data)
		for _, extra := range extras {
			h.Write(extra)
		}
		h.Sum(b[:0])
		return b
	}
}
