// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package vm

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
)

func testRandaoCtx(v byte) *evmtypes.BlockContext {
	h := types.Hash{v, 0xBE, 0xEF}
	return &evmtypes.BlockContext{PrevRanDao: &h}
}

func TestRandomnessPrecompileAddress(t *testing.T) {
	if _, ok := PrecompiledContractsRandomness[RandomnessAddress]; !ok {
		t.Fatal("randomness precompile not registered at 0x0302")
	}
	c := PrecompiledContractsRandomness[RandomnessAddress]
	if _, ok := c.(ContextAwarePrecompile); !ok {
		t.Fatal("randomness precompile must be context-aware")
	}
}

// TestRandomnessDeterministic pins the core property the redesign exists
// for: the output is a pure function of the header's PrevRanDao — same
// context in, same bytes out, on every node and at every replay.
func TestRandomnessDeterministic(t *testing.T) {
	c := &randomnessBeacon{}
	ctx := testRandaoCtx(0x11)
	a, err := c.RunWithContext(ctx, []byte{rngGetRandom})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.RunWithContext(testRandaoCtx(0x11), []byte{rngGetRandom})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same PrevRanDao must give identical output")
	}
	// And it must equal keccak256(prevRandao) exactly.
	want := crypto.Keccak256Hash((*ctx.PrevRanDao)[:])
	if !bytes.Equal(a, want[:]) {
		t.Fatal("output != keccak256(prevRandao)")
	}
	// Different randao → different output.
	d, err := c.RunWithContext(testRandaoCtx(0x22), []byte{rngGetRandom})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, d) {
		t.Fatal("different PrevRanDao must change the output")
	}
}

func TestRandomnessGetRandomInRange(t *testing.T) {
	c := &randomnessBeacon{}
	max := big.NewInt(1000)
	input := append([]byte{rngGetRandomInRange}, types.LeftPadBytes(max.Bytes(), 32)...)
	out, err := c.RunWithContext(testRandaoCtx(0x33), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 32 {
		t.Fatalf("output length %d", len(out))
	}
	v := new(big.Int).SetBytes(out)
	if v.Cmp(max) >= 0 {
		t.Fatalf("value %s out of range [0,%s)", v, max)
	}
}

func TestRandomnessGetRandomInRangeZeroMax(t *testing.T) {
	c := &randomnessBeacon{}
	input := append([]byte{rngGetRandomInRange}, make([]byte, 32)...)
	if _, err := c.RunWithContext(testRandaoCtx(0x44), input); err == nil {
		t.Fatal("zero max must error")
	}
}

func TestRandomnessGetRandomWithSeed(t *testing.T) {
	c := &randomnessBeacon{}
	seed1 := types.Hash{0x01}
	seed2 := types.Hash{0x02}
	ctx := testRandaoCtx(0x55)
	o1, err := c.RunWithContext(ctx, append([]byte{rngGetRandomWithSeed}, seed1[:]...))
	if err != nil {
		t.Fatal(err)
	}
	o2, err := c.RunWithContext(ctx, append([]byte{rngGetRandomWithSeed}, seed2[:]...))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(o1, o2) {
		t.Fatal("different seeds must give different outputs")
	}
	o1b, err := c.RunWithContext(testRandaoCtx(0x55), append([]byte{rngGetRandomWithSeed}, seed1[:]...))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(o1, o1b) {
		t.Fatal("same context+seed must be deterministic")
	}
}

func TestRandomnessGas(t *testing.T) {
	c := &randomnessBeacon{}
	if g := c.RequiredGas([]byte{rngGetRandom}); g != RandomnessGetRandomGas {
		t.Fatalf("getRandom gas %d", g)
	}
	if g := c.RequiredGas([]byte{rngGetRandomInRange}); g != RandomnessGetRandomInRangeGas {
		t.Fatalf("getRandomInRange gas %d", g)
	}
	if g := c.RequiredGas([]byte{rngGetRandomWithSeed}); g != RandomnessGetRandomGas {
		t.Fatalf("getRandomWithSeed gas %d", g)
	}
}

// TestRandomnessUnavailable: without a header randao the precompile
// fails deterministically — never a process-local fallback.
func TestRandomnessUnavailable(t *testing.T) {
	c := &randomnessBeacon{}
	if _, err := c.RunWithContext(&evmtypes.BlockContext{}, []byte{rngGetRandom}); err == nil {
		t.Fatal("nil PrevRanDao must error")
	}
	if _, err := c.RunWithContext(nil, []byte{rngGetRandom}); err == nil {
		t.Fatal("nil context must error")
	}
	// The context-free Run entry must never yield a value either.
	if _, err := c.Run([]byte{rngGetRandom}); err == nil {
		t.Fatal("plain Run must refuse")
	}
}
