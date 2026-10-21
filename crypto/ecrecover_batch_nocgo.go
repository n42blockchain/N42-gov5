// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build nacl || js || !cgo || gofuzz || darwin

package crypto

// EcrecoverIFMAEnabled is false: the IFMA kernel needs the cgo build.
var EcrecoverIFMAEnabled = false

// RecoverPubkeysBatch is Ecrecover per signature: errs[i] is nil and
// out[i] the 65-byte uncompressed key, or errs[i] is Ecrecover's error.
func RecoverPubkeysBatch(hashes [][32]byte, sigs [][65]byte, out [][65]byte, errs []error) {
	for i := range hashes {
		var pub []byte
		pub, errs[i] = Ecrecover(hashes[i][:], sigs[i][:])
		if errs[i] == nil {
			copy(out[i][:], pub)
		}
	}
}
