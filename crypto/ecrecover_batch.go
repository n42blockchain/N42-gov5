// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !nacl && !js && cgo && !gofuzz && !darwin

package crypto

import (
	"os"

	"github.com/erigontech/secp256k1"

	"github.com/n42blockchain/N42/crypto/secp256k1x8"
)

// EcrecoverIFMAEnabled routes RecoverPubkeysBatch through the eight-way
// AVX-512 IFMA kernel (crypto/secp256k1x8): on by default when the CPU
// has AVX-512 IFMA, off with N42_ECRECOVER_IFMA=0. Results are those of
// libsecp256k1 either way; the kernel hands every lane it does not handle
// back to libsecp256k1.
var EcrecoverIFMAEnabled = secp256k1x8.Supported && os.Getenv("N42_ECRECOVER_IFMA") != "0"

// ifmaMinLanes is the smallest group the kernel is given. A group costs
// the same with any number of live lanes, about 2.5 libsecp256k1
// recoveries, so groups of fewer lanes go to libsecp256k1 one by one.
const ifmaMinLanes = 3

// ifmaChunk bounds one kernel call, keeping the lane flags on the stack.
const ifmaChunk = 64

// RecoverPubkeysBatch recovers len(hashes) public keys: errs[i] is nil and
// out[i] the 65-byte uncompressed key when Ecrecover(hashes[i], sigs[i])
// would succeed with that key, and errs[i] is Ecrecover's error otherwise
// (out[i] then unspecified). sigs, out and errs must be at least as long as
// hashes. With EcrecoverIFMAEnabled, groups of eight go through the IFMA
// kernel; everything else, and every lane the kernel does not handle, goes
// through libsecp256k1.
func RecoverPubkeysBatch(hashes [][32]byte, sigs [][65]byte, out [][65]byte, errs []error) {
	n := len(hashes)
	_ = sigs[:n]
	_ = out[:n]
	_ = errs[:n]
	done := 0
	if EcrecoverIFMAEnabled {
		var handled [ifmaChunk]bool
		for done < n {
			m := min(n-done, ifmaChunk)
			if m < ifmaMinLanes {
				break
			}
			if tail := m % 8; tail != 0 && tail < ifmaMinLanes {
				m -= tail
			}
			secp256k1x8.Recover(hashes[done:done+m], sigs[done:done+m], out[done:done+m], handled[:m])
			for k := 0; k < m; k++ {
				i := done + k
				if handled[k] {
					errs[i] = nil
				} else {
					errs[i] = ecrecoverInto(&hashes[i], &sigs[i], &out[i])
				}
			}
			done += m
		}
	}
	for i := done; i < n; i++ {
		errs[i] = ecrecoverInto(&hashes[i], &sigs[i], &out[i])
	}
}

// ecrecoverInto is Ecrecover into out.
func ecrecoverInto(hash *[32]byte, sig *[65]byte, out *[65]byte) error {
	_, err := secp256k1.RecoverPubkeyWithContext(secp256k1.DefaultContext, hash[:], sig[:], out[:0])
	return err
}
