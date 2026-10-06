package vm

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// g65ValidG1Point and g65ValidG2Point are the first vector's valid encoded
// points from the EIP-2537 fixtures, reused here as "known good" halves so
// the second-operand decode error path can be exercised in isolation.
func g65FirstVectorInput(t *testing.T, filename string) []byte {
	t.Helper()
	v := loadEip2537Vectors(t, filename)
	if len(v) == 0 {
		t.Fatalf("%s has no vectors", filename)
	}
	for _, vec := range v {
		if vec.Expected != "" {
			b, err := hex.DecodeString(vec.Input)
			if err != nil {
				t.Fatalf("bad input hex: %v", err)
			}
			return b
		}
	}
	t.Fatalf("%s has no success vector", filename)
	return nil
}

func TestBLS12381G1AddRejectsInvalidSecondPoint(t *testing.T) {
	t.Parallel()
	good := g65FirstVectorInput(t, "blsG1Add.json")
	// Keep the first (valid) point, corrupt the second so it fails to decode.
	bad := append([]byte{}, good[:128]...)
	bad = append(bad, bytes.Repeat([]byte{0xFF}, 128)...)

	if _, err := (&bls12381G1Add{}).Run(bad); err == nil {
		t.Fatalf("Run() with a corrupted second G1 point must fail")
	}
}

func TestBLS12381G2AddRejectsInvalidSecondPoint(t *testing.T) {
	t.Parallel()
	good := g65FirstVectorInput(t, "blsG2Add.json")
	bad := append([]byte{}, good[:256]...)
	bad = append(bad, bytes.Repeat([]byte{0xFF}, 256)...)

	if _, err := (&bls12381G2Add{}).Run(bad); err == nil {
		t.Fatalf("Run() with a corrupted second G2 point must fail")
	}
}

func TestDecodeBLS12381FieldElementGnarkRejectsTopBytes(t *testing.T) {
	t.Parallel()

	in := make([]byte, 64)
	in[0] = 0x01 // violates the top-16-byte-zero rule

	if _, err := decodeBLS12381FieldElementGnark(in); err != errBLS12381InvalidFieldElementTopBytes {
		t.Fatalf("error = %v, want %v", err, errBLS12381InvalidFieldElementTopBytes)
	}
}

func TestDecodeBLS12381FieldElementGnarkRejectsWrongLength(t *testing.T) {
	t.Parallel()

	if _, err := decodeBLS12381FieldElementGnark(make([]byte, 32)); err == nil {
		t.Fatalf("expected an error for a field element of the wrong length")
	}
}
