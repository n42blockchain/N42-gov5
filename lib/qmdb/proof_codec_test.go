// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"testing"
)

// TestProofCodecRoundTrip: a proof Marshal→Unmarshal preserves every field and
// VerifyEncodedProof accepts it against the real root; tampering is rejected.
func TestProofCodecRoundTrip(t *testing.T) {
	tr := New()
	keys := make([]Hash, 0, 64)
	for i := 0; i < 64; i++ {
		var k Hash
		k[0] = byte(i)
		k[31] = byte(i * 7)
		v := []byte{byte(i), byte(i + 1), byte(i + 2)}
		tr.Set(k, v)
		keys = append(keys, k)
	}
	root := tr.Root()

	for _, k := range keys {
		p, ok := tr.GetProof(k)
		if !ok {
			t.Fatalf("GetProof(%x) missing", k)
		}
		blob := p.Marshal()

		// Decode and field-compare.
		dp, err := UnmarshalProof(blob)
		if err != nil {
			t.Fatalf("UnmarshalProof: %v", err)
		}
		if dp.KeyHash != p.KeyHash || dp.Slot != p.Slot || !bytes.Equal(dp.Value, p.Value) {
			t.Fatalf("decoded proof header diverged for %x", k)
		}
		if len(dp.UpperPath) != len(p.UpperPath) {
			t.Fatalf("upper path length diverged: %d vs %d", len(dp.UpperPath), len(p.UpperPath))
		}
		for i := range p.TwigPath {
			if dp.TwigPath[i] != p.TwigPath[i] {
				t.Fatalf("twig path[%d] diverged", i)
			}
		}

		// Verify the encoded proof against the world root.
		if !VerifyEncodedProof(root, blob) {
			t.Fatalf("VerifyEncodedProof rejected a valid proof for %x", k)
		}

		// Tamper the value — must be rejected.
		bad := make([]byte, len(blob))
		copy(bad, blob)
		bad[len(bad)-1] ^= 0xff
		if VerifyEncodedProof(root, bad) {
			t.Fatalf("VerifyEncodedProof accepted a tampered proof for %x", k)
		}
	}

	// A wrong root must be rejected.
	p, _ := tr.GetProof(keys[0])
	var wrong Hash
	wrong[0] = 0xab
	if VerifyEncodedProof(wrong, p.Marshal()) {
		t.Fatal("VerifyEncodedProof accepted against a wrong root")
	}

	// Garbage blob is rejected, not panicking.
	if _, err := UnmarshalProof([]byte{0x02, 0x00}); err == nil {
		t.Fatal("UnmarshalProof accepted a non-v1 blob")
	}
}
