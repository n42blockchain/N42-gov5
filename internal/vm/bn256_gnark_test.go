package vm

import (
	"bytes"
	"crypto/rand"
	"math/big"
	"testing"

	bn254 "github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/n42blockchain/N42/crypto/bn256"
)

// Reference: the cloudflare-backed implementations these replaced.
func cfBn256Add(input []byte) ([]byte, error) {
	x, err := cfCurvePoint(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	y, err := cfCurvePoint(getData(input, 64, 64))
	if err != nil {
		return nil, err
	}
	return new(bn256.G1).Add(x, y).Marshal(), nil
}

func cfBn256ScalarMul(input []byte) ([]byte, error) {
	p, err := cfCurvePoint(getData(input, 0, 64))
	if err != nil {
		return nil, err
	}
	return new(bn256.G1).ScalarMult(p, new(big.Int).SetBytes(getData(input, 64, 32))).Marshal(), nil
}

func cfBn256Pairing(input []byte) ([]byte, error) {
	if len(input)%192 > 0 {
		return nil, errBadPairingInput
	}
	var cs []*bn256.G1
	var ts []*bn256.G2
	for i := 0; i < len(input); i += 192 {
		c, err := cfCurvePoint(input[i : i+64])
		if err != nil {
			return nil, err
		}
		t, err := cfTwistPoint(input[i+64 : i+192])
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
		ts = append(ts, t)
	}
	if bn256.PairingCheck(cs, ts) {
		return true32Byte, nil
	}
	return false32Byte, nil
}

func cfCurvePoint(blob []byte) (*bn256.G1, error) {
	p := new(bn256.G1)
	if _, err := p.Unmarshal(blob); err != nil {
		return nil, err
	}
	return p, nil
}

func cfTwistPoint(blob []byte) (*bn256.G2, error) {
	p := new(bn256.G2)
	if _, err := p.Unmarshal(blob); err != nil {
		return nil, err
	}
	return p, nil
}

func randScalar(t *testing.T) *big.Int {
	k, err := rand.Int(rand.Reader, fr.Modulus())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func randG1(t *testing.T) []byte { return new(bn256.G1).ScalarBaseMult(randScalar(t)).Marshal() }
func randG2(t *testing.T) []byte { return new(bn256.G2).ScalarBaseMult(randScalar(t)).Marshal() }

func mustSame(t *testing.T, name string, in []byte, a func([]byte) ([]byte, error), b func([]byte) ([]byte, error)) {
	t.Helper()
	outA, errA := a(in)
	outB, errB := b(in)
	if (errA == nil) != (errB == nil) || !bytes.Equal(outA, outB) {
		t.Fatalf("%s mismatch on %x:\n cloudflare (%x, %v)\n gnark      (%x, %v)", name, in, outA, errA, outB, errB)
	}
}

func TestBn256GnarkMatchesCloudflare(t *testing.T) {
	inf := make([]byte, 64)
	var pBytes [32]byte
	fp.Modulus().FillBytes(pBytes[:])
	exceeds := append(append([]byte{}, pBytes[:]...), make([]byte, 32)...)
	notOnCurve := make([]byte, 64)
	notOnCurve[63] = 1
	big1 := bytes.Repeat([]byte{0xff}, 32)

	// ecAdd
	for i := 0; i < 64; i++ {
		a, b := randG1(t), randG1(t)
		neg := append([]byte{}, a...)
		var y fp.Element
		y.SetBytes(a[32:])
		y.Neg(&y)
		yb := y.Bytes()
		copy(neg[32:], yb[:])
		for _, in := range [][]byte{
			append(append([]byte{}, a...), b...),
			append(append([]byte{}, a...), a...),   // doubling
			append(append([]byte{}, a...), neg...), // P + (-P) = O
			append(append([]byte{}, a...), inf...),
			append(append([]byte{}, inf...), inf...),
			a, // short input: zero-padded second point
			append(append([]byte{}, a...), exceeds...),
			append(append([]byte{}, notOnCurve...), b...),
			nil,
		} {
			mustSame(t, "add", in, cfBn256Add, runBn256Add)
		}
	}
	// ecMul
	for i := 0; i < 64; i++ {
		p := randG1(t)
		for _, k := range [][]byte{
			randScalar(t).FillBytes(make([]byte, 32)),
			make([]byte, 32),                         // k = 0
			fr.Modulus().FillBytes(make([]byte, 32)), // k = r
			new(big.Int).Add(fr.Modulus(), big.NewInt(1)).FillBytes(make([]byte, 32)),
			big1, // 2^256-1
			{1},  // short scalar: zero padded → k = 1<<248? no: getData pads on the right
		} {
			mustSame(t, "mul", append(append([]byte{}, p...), k...), cfBn256ScalarMul, runBn256ScalarMul)
			mustSame(t, "mul", append(append([]byte{}, inf...), k...), cfBn256ScalarMul, runBn256ScalarMul)
		}
		mustSame(t, "mul", append(append([]byte{}, exceeds...), big1...), cfBn256ScalarMul, runBn256ScalarMul)
		mustSame(t, "mul", append(append([]byte{}, notOnCurve...), big1...), cfBn256ScalarMul, runBn256ScalarMul)
		mustSame(t, "mul", p[:40], cfBn256ScalarMul, runBn256ScalarMul)
	}
	// ecPairing
	g1, g2 := randG1(t), randG2(t)
	negG1 := append([]byte{}, g1...)
	var y fp.Element
	y.SetBytes(g1[32:])
	y.Neg(&y)
	yb := y.Bytes()
	copy(negG1[32:], yb[:])
	inf2 := make([]byte, 128)
	badG2 := append([]byte{}, g2...)
	badG2[127] ^= 1
	cases := [][]byte{
		nil,
		append(append([]byte{}, g1...), g2...), // e(P,Q) != 1
		append(append(append(append([]byte{}, g1...), g2...), negG1...), g2...), // e(P,Q)e(-P,Q) = 1
		append(append(append(append([]byte{}, g1...), g2...), g1...), g2...),    // e(P,Q)^2 != 1
		append(append([]byte{}, inf...), g2...),                                 // e(O,Q) = 1
		append(append([]byte{}, g1...), inf2...),                                // e(P,O) = 1
		append(append(append(append([]byte{}, inf...), g2...), g1...), inf2...), // all trivial
		append(append(append(append([]byte{}, g1...), g2...), inf...), g2...),   // one real, one trivial
		append(append([]byte{}, g1...), badG2...),                               // G2 off curve
		append(append([]byte{}, exceeds...), g2...),                             // coordinate >= p
		append(append([]byte{}, g1...), g2[:100]...),                            // bad length
	}
	for i := 0; i < 8; i++ {
		a, b, c, d := randG1(t), randG2(t), randG1(t), randG2(t)
		cases = append(cases, append(append(append(append([]byte{}, a...), b...), c...), d...))
	}
	for _, in := range cases {
		mustSame(t, "pairing", in, cfBn256Pairing, runBn256Pairing)
	}
}

func BenchmarkBn256PairingGnark(b *testing.B) {
	t := &testing.T{}
	in := append(append(append(append([]byte{}, randG1(t)...), randG2(t)...), randG1(t)...), randG2(t)...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runBn256Pairing(in)
	}
}

// TestBn256G2OutsideSubgroupRejected: a point on the twist curve that is not
// in the r-torsion must be rejected by both implementations (the twist has a
// large cofactor, so a random curve point is essentially never in it).
func TestBn256G2OutsideSubgroupRejected(t *testing.T) {
	_, _, _, gen := bn254.Generators()
	bTwist, tmp := gen.X, gen.X // values of the (internal) E2 type
	tmp.Square(&gen.X).Mul(&tmp, &gen.X)
	bTwist.Square(&gen.Y).Sub(&bTwist, &tmp) // b' = y^2 - x^3
	var q bn254.G2Affine
	found := false
	for tries := 0; tries < 256 && !found; tries++ {
		if _, err := q.X.SetRandom(); err != nil {
			t.Fatal(err)
		}
		rhs := gen.X
		rhs.Square(&q.X).Mul(&rhs, &q.X).Add(&rhs, &bTwist)
		if rhs.Legendre() != 1 {
			continue
		}
		q.Y.Sqrt(&rhs)
		if !q.IsOnCurve() {
			t.Fatal("constructed point is not on the twist")
		}
		if !q.IsInSubGroup() {
			found = true
		}
	}
	if !found {
		t.Skip("no twist point outside the subgroup found in 256 tries")
	}
	enc := make([]byte, 128)
	for i, e := range []*fp.Element{&q.X.A1, &q.X.A0, &q.Y.A1, &q.Y.A0} {
		b := e.Bytes()
		copy(enc[i*32:], b[:])
	}
	in := append(append([]byte{}, randG1(t)...), enc...)
	if _, err := cfBn256Pairing(in); err == nil {
		t.Fatal("cloudflare accepted a G2 point outside the subgroup")
	}
	if _, err := runBn256Pairing(in); err == nil {
		t.Fatal("gnark path accepted a G2 point outside the subgroup")
	}
}
