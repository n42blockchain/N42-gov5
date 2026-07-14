// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Differential validation for the Stack.PopPtr optimization: every opcode that
// was switched from Pop() (value copy) to PopPtr() (aliased slot) is executed as
// real bytecode through the full EVM interpreter and its result is compared to an
// independent uint256 oracle that never touches the stack. If PopPtr ever
// delivered a wrong or corrupted operand (aliasing / ordering regression), the
// interpreted result would diverge from the oracle. Long op-chains stress heavy
// backing-slot reuse — the exact condition an aliasing bug would surface under.

package runtime

import (
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// opcode bytes for the converted instructions.
const (
	opADD        = 0x01
	opMUL        = 0x02
	opSUB        = 0x03
	opDIV        = 0x04
	opSDIV       = 0x05
	opMOD        = 0x06
	opSMOD       = 0x07
	opADDMOD     = 0x08
	opMULMOD     = 0x09
	opSIGNEXTEND = 0x0b
	opLT         = 0x10
	opGT         = 0x11
	opSLT        = 0x12
	opSGT        = 0x13
	opEQ         = 0x14
	opAND        = 0x16
	opOR         = 0x17
	opXOR        = 0x18
	opBYTE       = 0x1a
	opSHL        = 0x1b
	opSHR        = 0x1c
	opSAR        = 0x1d
)

func push32(v *uint256.Int) []byte {
	b := v.Bytes32()
	return append([]byte{0x7f}, b[:]...) // PUSH32 <32 bytes>
}

// returnTop stores the current top-of-stack at mem[0] and returns mem[0:32].
var returnTop = []byte{
	0x60, 0x00, // PUSH1 0
	0x52,       // MSTORE (offset=0, value=top)
	0x60, 0x20, // PUSH1 32
	0x60, 0x00, // PUSH1 0
	0xf3, // RETURN mem[0:32]
}

// newState builds one reusable in-memory state; Execute recreates the contract
// account on every call, so a single state can back an entire test's runs.
func newState(t *testing.T) *state.IntraBlockState {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	return state.New(state.NewPlainState(tx, 1))
}

func runOne(t *testing.T, st *state.IntraBlockState, code []byte) *uint256.Int {
	t.Helper()
	cfg := &Config{GasLimit: 30_000_000, State: st}
	ret, _, err := Execute(code, nil, cfg, 0)
	if err != nil {
		t.Fatalf("EVM execution error: %v (code=%x)", err, code)
	}
	return new(uint256.Int).SetBytes(ret)
}

func randInt(r *rand.Rand) *uint256.Int {
	var b [32]byte
	r.Read(b[:])
	return new(uint256.Int).SetBytes(b[:])
}

// oracle mirrors the opcode's `y.Op(x, y)` semantics WITHOUT going through the
// stack: x is the value pushed last (top), y is pushed first (second-from-top).
func oracle2(op byte, x, y *uint256.Int) *uint256.Int {
	z := new(uint256.Int)
	switch op {
	case opADD:
		return z.Add(x, y)
	case opMUL:
		return z.Mul(x, y)
	case opSUB:
		return z.Sub(x, y)
	case opDIV:
		return z.Div(x, y)
	case opSDIV:
		return z.SDiv(x, y)
	case opMOD:
		return z.Mod(x, y)
	case opSMOD:
		return z.SMod(x, y)
	case opAND:
		return z.And(x, y)
	case opOR:
		return z.Or(x, y)
	case opXOR:
		return z.Xor(x, y)
	case opSIGNEXTEND: // num.ExtendSign(num, back); num=y, back=x
		return z.ExtendSign(y, x)
	case opLT:
		if x.Lt(y) {
			return z.SetOne()
		}
		return z
	case opGT:
		if x.Gt(y) {
			return z.SetOne()
		}
		return z
	case opSLT:
		if x.Slt(y) {
			return z.SetOne()
		}
		return z
	case opSGT:
		if x.Sgt(y) {
			return z.SetOne()
		}
		return z
	case opEQ:
		if x.Eq(y) {
			return z.SetOne()
		}
		return z
	case opBYTE: // val.Byte(th); val=y, th=x
		return z.Set(y).Byte(x)
	case opSHL: // value.Lsh(value, shift); value=y, shift=x
		if x.LtUint64(256) {
			return z.Lsh(y, uint(x.Uint64()))
		}
		return z
	case opSHR:
		if x.LtUint64(256) {
			return z.Rsh(y, uint(x.Uint64()))
		}
		return z
	case opSAR:
		if x.GtUint64(255) {
			if y.Sign() >= 0 {
				return z
			}
			return z.SetAllOne()
		}
		return z.SRsh(y, uint(x.Uint64()))
	}
	return z
}

// TestPopPtrBinaryOpsDiff runs every converted 2-operand opcode as real bytecode
// (PUSH32 y, PUSH32 x, OP) and compares the interpreted result to the oracle.
func TestPopPtrBinaryOpsDiff(t *testing.T) {
	ops := []byte{
		opADD, opMUL, opSUB, opDIV, opSDIV, opMOD, opSMOD,
		opSIGNEXTEND, opLT, opGT, opSLT, opSGT, opEQ,
		opAND, opOR, opXOR, opBYTE, opSHL, opSHR, opSAR,
	}
	st := newState(t)
	r := rand.New(rand.NewSource(0x50505452)) // "POPTR"
	const iters = 400

	for _, op := range ops {
		for i := 0; i < iters; i++ {
			x, y := randInt(r), randInt(r)
			// Bias some cases toward small/edge values (0, small shift, byte index).
			switch i % 5 {
			case 1:
				x = uint256.NewInt(uint64(r.Intn(300))) // shift/byte-index edges
			case 2:
				y = new(uint256.Int) // zero divisor / operand
			case 3:
				x = new(uint256.Int).SetAllOne()
			}
			code := append(append(push32(y), push32(x)...), op)
			code = append(code, returnTop...)

			got := runOne(t, st, code)
			want := oracle2(op, x, y)
			if !got.Eq(want) {
				t.Fatalf("op 0x%02x diverged: x=%s y=%s got=%s want=%s", op, x.Hex(), y.Hex(), got.Hex(), want.Hex())
			}
		}
	}
}

// TestPopPtrModOpsDiff covers the 3-operand ADDMOD/MULMOD.
func TestPopPtrModOpsDiff(t *testing.T) {
	st := newState(t)
	r := rand.New(rand.NewSource(0x4d4f44))
	const iters = 400
	for _, op := range []byte{opADDMOD, opMULMOD} {
		for i := 0; i < iters; i++ {
			a, b, c := randInt(r), randInt(r), randInt(r)
			if i%4 == 0 {
				c = new(uint256.Int) // zero modulus → result 0
			}
			// push c, push b, push a  =>  top=a(x), next=b(y), next2=c(z)
			code := append(append(append(push32(c), push32(b)...), push32(a)...), op)
			code = append(code, returnTop...)

			got := runOne(t, st, code)
			want := new(uint256.Int)
			if op == opADDMOD {
				want.AddMod(a, b, c)
			} else {
				want.MulMod(a, b, c)
			}
			if !got.Eq(want) {
				t.Fatalf("op 0x%02x diverged: a=%s b=%s c=%s got=%s want=%s", op, a.Hex(), b.Hex(), c.Hex(), got.Hex(), want.Hex())
			}
		}
	}
}

// TestPopPtrChainDiff stresses backing-slot reuse: a long chain of binary ops
// keeps a running accumulator on the stack (PUSH k; OP), the exact pattern where
// a PopPtr aliasing bug — a stale pointer overwritten by a later Push — would
// corrupt a downstream result. The whole chain is cross-checked step-by-step
// against the oracle.
func TestPopPtrChainDiff(t *testing.T) {
	chainOps := []byte{opADD, opMUL, opSUB, opXOR, opAND, opOR, opDIV, opMOD}
	st := newState(t)
	r := rand.New(rand.NewSource(0xC4A15))

	for trial := 0; trial < 50; trial++ {
		acc := randInt(r)
		code := push32(acc)
		want := new(uint256.Int).Set(acc)

		for step := 0; step < 64; step++ {
			k := randInt(r)
			op := chainOps[r.Intn(len(chainOps))]
			code = append(code, push32(k)...) // k becomes top (x); acc is survivor (y)
			code = append(code, op)
			// mirror y.Op(x, y): x = k (top), y = acc (running survivor)
			want = oracle2(op, k, want)
		}
		code = append(code, returnTop...)

		got := runOne(t, st, code)
		if !got.Eq(want) {
			t.Fatalf("chain trial %d diverged: got=%s want=%s", trial, got.Hex(), want.Hex())
		}
	}
}
