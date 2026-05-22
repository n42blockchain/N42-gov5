// verkle-msm-spike measures multi-scalar multiplication latency on the
// three curves relevant to a Verkle commitment swap decision:
//
//   - Banderwagon (current go-verkle backend; IPA-based)
//   - BLS12-381 G1 (KZG candidate; same as EIP-4844 blob commitments)
//   - BN254 G1 (KZG candidate; same as Eth precompiles)
//
// MSM dominates the Verkle commit hot path: each internal node update
// is one MSM of width ≤ 256 (one per modified child slot). For sparse
// blocks the practical width is 1-16; for state-heavy blocks closer to
// the full 256. We sweep both regimes.
package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"runtime"
	"sort"
	"time"

	"github.com/consensys/gnark-crypto/ecc"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	bls12381fr "github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	bn254 "github.com/consensys/gnark-crypto/ecc/bn254"
	bn254fr "github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/crate-crypto/go-ipa/banderwagon"
	bsfr "github.com/crate-crypto/go-ipa/bandersnatch/fr"
)

const (
	samples       = 50  // independent timing samples (for median/p95)
	innerPerBatch = 100 // MSM calls per timing sample — averages out Windows clock granularity
	warmup        = 5
)

var widths = []int{1, 4, 16, 64, 256}

type result struct {
	curve  string
	n      int
	mean   time.Duration
	median time.Duration
	p95    time.Duration
}

func main() {
	fmt.Printf("verkle-msm-spike — GOMAXPROCS=%d  samples=%d  innerPerBatch=%d  (NbTasks=1)\n\n",
		runtime.GOMAXPROCS(0), samples, innerPerBatch)

	var results []result
	for _, n := range widths {
		results = append(results, benchBanderwagon(n))
		results = append(results, benchBLS12381(n))
		results = append(results, benchBN254(n))
	}
	printTable(results)
}

func benchBanderwagon(n int) result {
	points := make([]banderwagon.Element, n)
	scalars := make([]bsfr.Element, n)
	gen := banderwagon.Generator
	for i := 0; i < n; i++ {
		var s, ss bsfr.Element
		var buf [32]byte
		_, _ = rand.Read(buf[:])
		s.SetBytes(buf[:])
		var p banderwagon.Element
		p.ScalarMul(&gen, &s)
		points[i] = p
		_, _ = rand.Read(buf[:])
		ss.SetBytes(buf[:])
		scalars[i] = ss
	}

	var out banderwagon.Element
	for i := 0; i < warmup; i++ {
		_, _ = out.MultiExp(points, scalars, banderwagon.MultiExpConfig{NbTasks: 1})
	}
	durs := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		for k := 0; k < innerPerBatch; k++ {
			_, _ = out.MultiExp(points, scalars, banderwagon.MultiExpConfig{NbTasks: 1})
		}
		durs[i] = time.Since(t0) / innerPerBatch
	}
	return summarize("Banderwagon", n, durs)
}

func benchBLS12381(n int) result {
	points := make([]bls12381.G1Affine, n)
	scalars := make([]bls12381fr.Element, n)
	_, _, genAff, _ := bls12381.Generators()
	for i := 0; i < n; i++ {
		var s, ss bls12381fr.Element
		var buf [32]byte
		_, _ = rand.Read(buf[:])
		s.SetBytes(buf[:])
		var pj bls12381.G1Jac
		var pjGen bls12381.G1Jac
		pjGen.FromAffine(&genAff)
		bi := new(big.Int)
		s.BigInt(bi)
		pj.ScalarMultiplication(&pjGen, bi)
		points[i].FromJacobian(&pj)
		_, _ = rand.Read(buf[:])
		ss.SetBytes(buf[:])
		scalars[i] = ss
	}

	var out bls12381.G1Jac
	for i := 0; i < warmup; i++ {
		_, _ = out.MultiExp(points, scalars, ecc.MultiExpConfig{NbTasks: 1})
	}
	durs := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		for k := 0; k < innerPerBatch; k++ {
			_, _ = out.MultiExp(points, scalars, ecc.MultiExpConfig{NbTasks: 1})
		}
		durs[i] = time.Since(t0) / innerPerBatch
	}
	return summarize("BLS12-381 G1", n, durs)
}

func benchBN254(n int) result {
	points := make([]bn254.G1Affine, n)
	scalars := make([]bn254fr.Element, n)
	_, _, genAff, _ := bn254.Generators()
	for i := 0; i < n; i++ {
		var s, ss bn254fr.Element
		var buf [32]byte
		_, _ = rand.Read(buf[:])
		s.SetBytes(buf[:])
		var pj bn254.G1Jac
		var pjGen bn254.G1Jac
		pjGen.FromAffine(&genAff)
		bi := new(big.Int)
		s.BigInt(bi)
		pj.ScalarMultiplication(&pjGen, bi)
		points[i].FromJacobian(&pj)
		_, _ = rand.Read(buf[:])
		ss.SetBytes(buf[:])
		scalars[i] = ss
	}

	var out bn254.G1Jac
	for i := 0; i < warmup; i++ {
		_, _ = out.MultiExp(points, scalars, ecc.MultiExpConfig{NbTasks: 1})
	}
	durs := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		for k := 0; k < innerPerBatch; k++ {
			_, _ = out.MultiExp(points, scalars, ecc.MultiExpConfig{NbTasks: 1})
		}
		durs[i] = time.Since(t0) / innerPerBatch
	}
	return summarize("BN254 G1", n, durs)
}

func summarize(curve string, n int, s []time.Duration) result {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return result{
		curve:  curve,
		n:      n,
		mean:   sum / time.Duration(len(s)),
		median: s[len(s)/2],
		p95:    s[(len(s)*95)/100],
	}
}

func printTable(rs []result) {
	fmt.Printf("%-14s %5s %12s %12s %12s %14s\n",
		"curve", "N", "mean", "median", "p95", "x vs B-wagon")
	fmt.Println("------------------------------------------------------------------------------")

	baseline := map[int]time.Duration{}
	for _, r := range rs {
		if r.curve == "Banderwagon" {
			baseline[r.n] = r.median
		}
	}
	for _, r := range rs {
		ratio := float64(r.median) / float64(baseline[r.n])
		fmt.Printf("%-14s %5d %12s %12s %12s %14s\n",
			r.curve, r.n, fmtDur(r.mean), fmtDur(r.median), fmtDur(r.p95),
			fmt.Sprintf("%.2fx", ratio))
	}
	fmt.Println()
	fmt.Println("Interpretation:")
	fmt.Println("  - N=1..16  → typical sparse block (few touched children per internal node)")
	fmt.Println("  - N=64..256 → state-heavy block / full leaf-level recommit")
	fmt.Println("  - 'x vs B-wagon' > 1.0 means KZG candidate is SLOWER on commit hot path")
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fµs", float64(d.Nanoseconds())/1000)
	default:
		return fmt.Sprintf("%.3fms", float64(d.Nanoseconds())/1e6)
	}
}
