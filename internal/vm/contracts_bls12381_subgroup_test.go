package vm

import (
	"errors"
	"math/big"
	"testing"
)

func mustBigFromHex(t *testing.T, s string) *big.Int {
	t.Helper()

	v, ok := new(big.Int).SetString(s, 0)
	if !ok {
		t.Fatalf("failed to parse %q", s)
	}
	return v
}

func encodeBLSFieldElement(v *big.Int) []byte {
	out := make([]byte, 64)
	if v == nil {
		return out
	}
	b := v.Bytes()
	copy(out[len(out)-len(b):], b)
	return out
}

func encodeBLSG1Point(x, y *big.Int) []byte {
	out := make([]byte, 0, 128)
	out = append(out, encodeBLSFieldElement(x)...)
	out = append(out, encodeBLSFieldElement(y)...)
	return out
}

func encodeBLSG2Point(x0, x1, y0, y1 *big.Int) []byte {
	out := make([]byte, 0, 256)
	out = append(out, encodeBLSFieldElement(x0)...)
	out = append(out, encodeBLSFieldElement(x1)...)
	out = append(out, encodeBLSFieldElement(y0)...)
	out = append(out, encodeBLSFieldElement(y1)...)
	return out
}

func encodeBLSScalar(v uint64) []byte {
	out := make([]byte, 32)
	new(big.Int).SetUint64(v).FillBytes(out)
	return out
}

func TestBLS12381G1MultiExpRejectsPointsOutsideSubgroup(t *testing.T) {
	t.Parallel()

	input := append(encodeBLSG1Point(big.NewInt(0), big.NewInt(2)), encodeBLSScalar(1)...)

	_, err := (&bls12381G1MultiExp{}).Run(input)
	if !errors.Is(err, errBLS12381G1PointSubgroup) {
		t.Fatalf("Run error = %v, want %v", err, errBLS12381G1PointSubgroup)
	}
}

func TestBLS12381G2MultiExpRejectsPointsOutsideSubgroup(t *testing.T) {
	t.Parallel()

	input := append(
		encodeBLSG2Point(
			mustBigFromHex(t, "0x1"),
			mustBigFromHex(t, "0x1"),
			mustBigFromHex(t, "0x17faa6201231304f270b858dad9462089f2a5b83388e4b10773abc1eef6d193b9fce4e8ea2d9d28e3c3a315aa7de14ca"),
			mustBigFromHex(t, "0x0cc12449be6ac4e7f367e7242250427c4fb4c39325d3164ad397c1837a90f0ea1a534757df374dd6569345eb41ed76e"),
		),
		encodeBLSScalar(1)...,
	)

	_, err := (&bls12381G2MultiExp{}).Run(input)
	if !errors.Is(err, errBLS12381G2PointSubgroup) {
		t.Fatalf("Run error = %v, want %v", err, errBLS12381G2PointSubgroup)
	}
}
