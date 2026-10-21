// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build cgo && amd64 && !gofuzz

package secp256k1x8

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/erigontech/secp256k1"
)

var (
	curveN = mustHex("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
	curveP = mustHex("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F")
)

func mustHex(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic(s)
	}
	return v
}

func put32(b []byte, v *big.Int) {
	m := new(big.Int).Mod(v, new(big.Int).Lsh(big.NewInt(1), 256))
	m.FillBytes(b[:32])
}

func skipUnsupported(t testing.TB) {
	if !Supported {
		t.Skip("AVX-512 IFMA not supported on this CPU")
	}
}

// diffBatch runs the kernel over the batch and compares every lane with
// libsecp256k1: a handled lane must be one libsecp256k1 recovers, with the
// same key. It returns how many lanes the kernel handled and how many it
// sent back that libsecp256k1 recovers.
func diffBatch(msgs [][32]byte, sigs [][65]byte) (count, missed int, err error) {
	n := len(msgs)
	pubs := make([][65]byte, n)
	handled := make([]bool, n)
	Recover(msgs, sigs, pubs, handled)
	for i := 0; i < n; i++ {
		want, err := secp256k1.RecoverPubkey(msgs[i][:], sigs[i][:])
		if !handled[i] {
			if err == nil {
				missed++
			}
			continue
		}
		count++
		if err != nil {
			return count, missed, fmt.Errorf("lane %d: handled, but libsecp256k1 fails (%v)\nmsg %x\nsig %x", i, err, msgs[i], sigs[i])
		}
		if !bytes.Equal(want, pubs[i][:]) {
			return count, missed, fmt.Errorf("lane %d: key mismatch\nmsg %x\nsig %x\nwant %x\ngot  %x", i, msgs[i], sigs[i], want, pubs[i])
		}
	}
	return count, missed, nil
}

func checkBatch(t testing.TB, msgs [][32]byte, sigs [][65]byte) int {
	t.Helper()
	c, _, err := diffBatch(msgs, sigs)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// randomSigs fills a batch: three quarters signed by random keys, a quarter
// random (r, s) with recovery id 0 or 1 (r on the curve about half the time).
func randomSigs(rng *rand.Rand, msgs [][32]byte, sigs [][65]byte) {
	for i := range msgs {
		fillRand(rng, msgs[i][:])
		if rng.IntN(4) != 0 {
			var key [32]byte
			for {
				fillRand(rng, key[:])
				k := new(big.Int).SetBytes(key[:])
				if k.Sign() > 0 && k.Cmp(curveN) < 0 {
					break
				}
			}
			sig, err := secp256k1.Sign(msgs[i][:], key[:])
			if err != nil {
				panic(err)
			}
			copy(sigs[i][:], sig)
		} else {
			fillRand(rng, sigs[i][:64])
			sigs[i][64] = byte(rng.IntN(2))
		}
	}
}

func fillRand(rng *rand.Rand, b []byte) {
	for i := 0; i < len(b); i += 8 {
		var w [8]byte
		binary.LittleEndian.PutUint64(w[:], rng.Uint64())
		copy(b[i:], w[:])
	}
}

// TestRecoverDifferential compares the kernel with libsecp256k1 over
// N42_X8_DIFF_N random signatures (1,000,000 by default, 20,000 with
// -short).
func TestRecoverDifferential(t *testing.T) {
	skipUnsupported(t)
	total := 1_000_000
	if testing.Short() {
		total = 20_000
	}
	if s := os.Getenv("N42_X8_DIFF_N"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		total = v
	}
	const batch = 4096
	workers := runtime.GOMAXPROCS(0)
	var (
		next, handled, checked, missed atomic.Int64
		wg                             sync.WaitGroup
		failed                         atomic.Bool
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
			msgs := make([][32]byte, batch)
			sigs := make([][65]byte, batch)
			for !failed.Load() {
				lo := next.Add(batch) - batch
				if lo >= int64(total) {
					return
				}
				m := min(int64(batch), int64(total)-lo)
				randomSigs(rng, msgs[:m], sigs[:m])
				// Ragged sizes exercise partial groups.
				sub := int(m)
				if rng.IntN(3) == 0 {
					sub = int(m) - rng.IntN(8)
				}
				for _, part := range [][2]int{{0, sub}, {sub, int(m)}} {
					if part[0] == part[1] {
						continue
					}
					c, mi, err := diffBatch(msgs[part[0]:part[1]], sigs[part[0]:part[1]])
					handled.Add(int64(c))
					missed.Add(int64(mi))
					if err != nil {
						t.Error(err)
						failed.Store(true)
					}
				}
				checked.Add(m)
			}
		}(uint64(w) + 1)
	}
	wg.Wait()
	t.Logf("checked %d signatures, %d handled by the kernel, %d sent back", checked.Load(), handled.Load(),
		checked.Load()-handled.Load())
	// Random signatures never hit a degenerate case: every one libsecp256k1
	// recovers must have been handled.
	if missed.Load() != 0 {
		t.Fatalf("kernel sent back %d signatures libsecp256k1 recovers", missed.Load())
	}
}

// edgeSigs returns signatures around every boundary the kernel checks.
func edgeSigs(t testing.TB) (msgs [][32]byte, sigs [][65]byte) {
	one := big.NewInt(1)
	two256 := new(big.Int).Lsh(one, 256)
	rs := []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(2), big.NewInt(3),
		new(big.Int).Sub(curveN, one), curveN, new(big.Int).Add(curveN, one),
		new(big.Int).Sub(curveP, curveN), new(big.Int).Sub(new(big.Int).Sub(curveP, curveN), one),
		new(big.Int).Sub(curveP, one), curveP, new(big.Int).Sub(two256, one),
		mustHex("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798"), // x(G)
	}
	ss := []*big.Int{
		big.NewInt(0), big.NewInt(1), new(big.Int).Rsh(curveN, 1), new(big.Int).Add(new(big.Int).Rsh(curveN, 1), one),
		new(big.Int).Sub(curveN, one), curveN, new(big.Int).Sub(two256, one), mustHex("1234567890abcdef"),
	}
	hs := []*big.Int{
		big.NewInt(0), big.NewInt(1), new(big.Int).Sub(curveN, one), curveN, new(big.Int).Add(curveN, one),
		new(big.Int).Sub(two256, one), mustHex("deadbeef"),
	}
	for _, r := range rs {
		for _, s := range ss {
			for _, h := range hs {
				for v := 0; v < 5; v++ {
					var m [32]byte
					var sg [65]byte
					put32(m[:], h)
					put32(sg[:32], r)
					put32(sg[32:64], s)
					sg[64] = byte(v)
					msgs = append(msgs, m)
					sigs = append(sigs, sg)
				}
			}
		}
	}
	// Valid signatures with s flipped high and with the other parity.
	rng := rand.New(rand.NewPCG(7, 7))
	vm := make([][32]byte, 64)
	vs := make([][65]byte, 64)
	randomSigs(rng, vm, vs)
	for i := range vm {
		s := new(big.Int).SetBytes(vs[i][32:64])
		hi := vs[i]
		put32(hi[32:64], new(big.Int).Sub(curveN, s))
		hi[64] ^= 1
		flip := vs[i]
		flip[64] ^= 1
		msgs = append(msgs, vm[i], vm[i], vm[i])
		sigs = append(sigs, vs[i], hi, flip)
	}
	// Results at infinity and degenerate final additions: with R = kG,
	// z = s k gives sR - zG = O, and z = -s k gives u2 R = u1 G.
	for k := int64(1); k <= 8; k++ {
		kk := big.NewInt(k*7919 + 3)
		x, y := secp256k1.S256().ScalarBaseMult(kk.Bytes())
		for _, sv := range []int64{1, 5, 99991} {
			s := big.NewInt(sv)
			for _, neg := range []bool{false, true} {
				z := new(big.Int).Mul(s, kk)
				if neg {
					z.Neg(z)
				}
				z.Mod(z, curveN)
				var m [32]byte
				var sg [65]byte
				put32(m[:], z)
				put32(sg[:32], x)
				put32(sg[32:64], s)
				sg[64] = byte(y.Bit(0))
				msgs = append(msgs, m)
				sigs = append(sigs, sg)
			}
		}
	}
	return msgs, sigs
}

func TestRecoverEdgeCases(t *testing.T) {
	skipUnsupported(t)
	msgs, sigs := edgeSigs(t)
	handled := checkBatch(t, msgs, sigs)
	t.Logf("%d edge-case signatures, %d handled by the kernel", len(msgs), handled)
	// Every lane on its own in a group, and in a group with seven valid
	// neighbours, so no lane's outcome depends on its group.
	rng := rand.New(rand.NewPCG(11, 13))
	nm := make([][32]byte, 8)
	ns := make([][65]byte, 8)
	for i := range msgs {
		randomSigs(rng, nm, ns)
		j := rng.IntN(8)
		nm[j], ns[j] = msgs[i], sigs[i]
		checkBatch(t, nm, ns)
		checkBatch(t, msgs[i:i+1], sigs[i:i+1])
	}
}

// TestRecoverDegenerateNotHandled pins the classes the kernel must hand
// back: recovery ids 2 and 3, out-of-range r and s, and results at
// infinity.
func TestRecoverDegenerateNotHandled(t *testing.T) {
	skipUnsupported(t)
	msgs, sigs := edgeSigs(t)
	pubs := make([][65]byte, len(msgs))
	handled := make([]bool, len(msgs))
	Recover(msgs, sigs, pubs, handled)
	for i := range msgs {
		r := new(big.Int).SetBytes(sigs[i][:32])
		s := new(big.Int).SetBytes(sigs[i][32:64])
		bad := sigs[i][64] >= 2 || r.Sign() == 0 || s.Sign() == 0 || r.Cmp(curveN) >= 0 || s.Cmp(curveN) >= 0
		if bad && handled[i] {
			t.Fatalf("lane %d handled: sig %x", i, sigs[i])
		}
	}
}

// FuzzRecover compares one fuzzed signature, in a group with seven valid
// ones, with libsecp256k1.
func FuzzRecover(f *testing.F) {
	msgs, sigs := edgeSigs(f)
	for i := 0; i < len(msgs); i += 7 {
		f.Add(msgs[i][:], sigs[i][:])
	}
	f.Fuzz(func(t *testing.T, msg, sig []byte) {
		skipUnsupported(t)
		if len(msg) != 32 || len(sig) != 65 {
			return
		}
		rng := rand.New(rand.NewPCG(uint64(sig[0]), uint64(msg[0])))
		nm := make([][32]byte, 8)
		ns := make([][65]byte, 8)
		randomSigs(rng, nm, ns)
		j := int(sig[1] % 8)
		copy(nm[j][:], msg)
		copy(ns[j][:], sig)
		checkBatch(t, nm, ns)
	})
}

func benchSigs(b *testing.B, n int) ([][32]byte, [][65]byte) {
	rng := rand.New(rand.NewPCG(1, 2))
	msgs := make([][32]byte, n)
	sigs := make([][65]byte, n)
	for i := range msgs {
		fillRand(rng, msgs[i][:])
		var key [32]byte
		fillRand(rng, key[:])
		key[0] &= 0x7f
		sig, err := secp256k1.Sign(msgs[i][:], key[:])
		if err != nil {
			b.Fatal(err)
		}
		copy(sigs[i][:], sig)
	}
	return msgs, sigs
}

// BenchmarkRecoverLibsecp is one libsecp256k1 recovery per op.
func BenchmarkRecoverLibsecp(b *testing.B) {
	msgs, sigs := benchSigs(b, 64)
	var out [65]byte
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := i & 63
		if _, err := secp256k1.RecoverPubkeyWithContext(secp256k1.DefaultContext, msgs[k][:], sigs[k][:], out[:0]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecoverX8 is the kernel; ns/op is per signature.
func BenchmarkRecoverX8(b *testing.B) {
	skipUnsupported(b)
	for _, n := range []int{8, 64} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			msgs, sigs := benchSigs(b, n)
			pubs := make([][65]byte, n)
			handled := make([]bool, n)
			Recover(msgs, sigs, pubs, handled)
			b.ResetTimer()
			for i := 0; i < b.N; i += n {
				Recover(msgs, sigs, pubs, handled)
			}
		})
	}
}
