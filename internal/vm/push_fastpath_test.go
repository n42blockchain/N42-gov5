// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package vm

import (
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// referencePush is the pre-fast-path semantics for every PUSHn: take up to n
// immediate bytes, right-pad with zeros past the end of code, interpret as a
// big-endian integer.
func referencePush(code []byte, pc uint64, n int) uint256.Int {
	codeLen := len(code)
	start := int(pc + 1)
	if start >= codeLen {
		start = codeLen
	}
	end := start + n
	if end >= codeLen {
		end = codeLen
	}
	var v uint256.Int
	v.SetBytes(types.RightPadBytes(code[start:end], n))
	return v
}

// TestPushMatchesReference runs every PUSH1..PUSH32 over random code at random
// pcs, deliberately including pcs whose immediate runs off the end of code and
// pcs at the very last byte, and checks the pushed value and the pc advance
// against the reference.
func TestPushMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for n := 1; n <= 32; n++ {
		var op executionFunc
		if n == 1 {
			op = opPush1
		} else {
			op = makePush(uint64(n), n)
		}
		for round := 0; round < 400; round++ {
			code := make([]byte, 1+rng.Intn(80))
			rng.Read(code)
			pc := uint64(rng.Intn(len(code)))
			if round%5 == 0 {
				pc = uint64(len(code) - 1) // immediate entirely past the end
			}
			want := referencePush(code, pc, n)

			st := stack.New()
			scope := &ScopeContext{Stack: st, Contract: &Contract{Code: code}}
			gotPC := pc
			if _, err := op(&gotPC, nil, scope); err != nil {
				t.Fatalf("PUSH%d: unexpected error %v", n, err)
			}
			if st.Len() != 1 {
				t.Fatalf("PUSH%d: stack len %d", n, st.Len())
			}
			if got := st.Peek(); !got.Eq(&want) {
				t.Fatalf("PUSH%d code=%x pc=%d: got %s want %s", n, code, pc, got.Hex(), want.Hex())
			}
			// The interpreter loop adds the final +1; the op itself advances
			// by the immediate width. opPush1 advances by exactly 1 as well.
			if gotPC != pc+uint64(n) {
				t.Fatalf("PUSH%d: pc advanced to %d, want %d", n, gotPC, pc+uint64(n))
			}
			stack.ReturnNormalStack(st)
		}
	}
}

func BenchmarkPush4(b *testing.B) {
	op := makePush(4, 4)
	code := []byte{0x63, 0xa9, 0x05, 0x9c, 0xbb, 0x00}
	st := stack.New()
	scope := &ScopeContext{Stack: st, Contract: &Contract{Code: code}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		_, _ = op(&pc, nil, scope)
		st.Reset()
	}
}

func BenchmarkPush20(b *testing.B) {
	op := makePush(20, 20)
	code := make([]byte, 22)
	st := stack.New()
	scope := &ScopeContext{Stack: st, Contract: &Contract{Code: code}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		_, _ = op(&pc, nil, scope)
		st.Reset()
	}
}
