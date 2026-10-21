// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !nacl && !js && cgo && !gofuzz && !darwin

package crypto

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
)

// batchInputs returns n signatures: mostly valid, every seventh with a
// corrupted r, every eleventh with recovery id 2 or 4, every thirteenth
// with a zero s.
func batchInputs(t *testing.T, n int) ([][32]byte, [][65]byte) {
	rng := rand.New(rand.NewPCG(uint64(n), 3))
	hashes := make([][32]byte, n)
	sigs := make([][65]byte, n)
	for i := 0; i < n; i++ {
		key, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		for k := range hashes[i] {
			hashes[i][k] = byte(rng.Uint32())
		}
		sig, err := Sign(hashes[i][:], key)
		if err != nil {
			t.Fatal(err)
		}
		copy(sigs[i][:], sig)
		switch {
		case i%7 == 3:
			sigs[i][5] ^= 0x40
		case i%11 == 4:
			sigs[i][64] = 2 + byte(i%3)
		case i%13 == 5:
			clear(sigs[i][32:64])
		}
	}
	return hashes, sigs
}

func TestRecoverPubkeysBatchMatchesEcrecover(t *testing.T) {
	defer func(v bool) { EcrecoverIFMAEnabled = v }(EcrecoverIFMAEnabled)
	for _, ifma := range []bool{false, true} {
		EcrecoverIFMAEnabled = ifma && EcrecoverIFMAEnabled
		for _, n := range []int{0, 1, 2, 3, 7, 8, 9, 10, 11, 16, 63, 64, 65, 66, 67, 130, 200} {
			hashes, sigs := batchInputs(t, n)
			out := make([][65]byte, n)
			errs := make([]error, n)
			RecoverPubkeysBatch(hashes, sigs, out, errs)
			for i := 0; i < n; i++ {
				want, werr := Ecrecover(hashes[i][:], sigs[i][:])
				if !errors.Is(errs[i], werr) || (werr == nil) != (errs[i] == nil) {
					t.Fatalf("ifma=%v n=%d lane %d: err %v, want %v", ifma, n, i, errs[i], werr)
				}
				if werr == nil && !bytes.Equal(out[i][:], want) {
					t.Fatalf("ifma=%v n=%d lane %d: key %x, want %x", ifma, n, i, out[i], want)
				}
			}
		}
	}
}

// BenchmarkRecoverPubkeysBatch is ns per signature through the batch API.
func BenchmarkRecoverPubkeysBatch(b *testing.B) {
	const n = 64
	hashes := make([][32]byte, n)
	sigs := make([][65]byte, n)
	for i := range hashes {
		key, _ := GenerateKey()
		hashes[i] = Keccak256Hash([]byte{byte(i)})
		sig, _ := Sign(hashes[i][:], key)
		copy(sigs[i][:], sig)
	}
	out := make([][65]byte, n)
	errs := make([]error, n)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += n {
		RecoverPubkeysBatch(hashes, sigs, out, errs)
	}
}
