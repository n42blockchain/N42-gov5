package vm

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestSha3MemoHitMatchesMiss: the KECCAK256 memo for 64-byte inputs must
// return the same digest on a hit as on the first computation, must stay
// bounded once full, and must never serve a wrong entry.
func TestSha3MemoHitMatchesMiss(t *testing.T) {
	cfg := testParisCreateChainConfig()
	// mstore(0, calldata[0:32]); mstore(32, calldata[32:64]); return keccak(mem[0:64])
	code := []byte{
		byte(PUSH1), 0, byte(CALLDATALOAD), byte(PUSH1), 0, byte(MSTORE),
		byte(PUSH1), 32, byte(CALLDATALOAD), byte(PUSH1), 32, byte(MSTORE),
		byte(PUSH1), 64, byte(PUSH1), 0, byte(KECCAK256),
		byte(PUSH1), 0, byte(MSTORE), byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN),
	}
	evm, ibs := newAuditEVM(t, cfg, Config{})
	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	target := types.HexToAddress("0x8000000000000000000000000000000000000008")
	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, code)
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)
	in := evm.interpreter.(*EVMInterpreter)

	call := func(input []byte) []byte {
		ret, _, err := evm.Call(AccountRef(caller), target, input, 100000, uint256.NewInt(0), false)
		if err != nil {
			t.Fatal(err)
		}
		return ret
	}
	inputs := [][]byte{make([]byte, 64), bytes.Repeat([]byte{0xab}, 64)}
	inputs = append(inputs, append(bytes.Repeat([]byte{0xab}, 63), 0xac))
	for round := 0; round < 3; round++ {
		for _, input := range inputs {
			if got, want := call(input), crypto.Keccak256(input); !bytes.Equal(got, want) {
				t.Fatalf("round %d input %x: got %x want %x", round, input, got, want)
			}
		}
	}
	if len(in.sha3Memo) != len(inputs) {
		t.Fatalf("memo holds %d entries, want %d", len(in.sha3Memo), len(inputs))
	}
	// Fill to the bound with junk and check nothing more is admitted while
	// results stay right.
	for i := 0; len(in.sha3Memo) < sha3MemoMax; i++ {
		var k [64]byte
		k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
		k[63] = 0x77
		in.sha3Memo[k] = types.Hash{}
	}
	fresh := append(bytes.Repeat([]byte{0x11}, 63), 0x12)
	if got, want := call(fresh), crypto.Keccak256(fresh); !bytes.Equal(got, want) {
		t.Fatalf("full memo: got %x want %x", got, want)
	}
	if len(in.sha3Memo) != sha3MemoMax {
		t.Fatalf("memo grew past its bound: %d", len(in.sha3Memo))
	}
	// A 65-byte input must not touch the memo path at all.
	code65 := append([]byte{}, code...)
	code65[13] = 65
	ibs.SetCode(target, code65)
	if got, want := call(inputs[1]), crypto.Keccak256(append(inputs[1], 0)); !bytes.Equal(got, want) {
		t.Fatalf("65-byte hash: got %x want %x", got, want)
	}
}

// TestContractAddressCacheAcrossCallKinds checks the cached Contract.addr
// against ADDRESS and SLOAD semantics under CALL, CALLCODE, DELEGATECALL,
// STATICCALL and CREATE: the storage context must follow the call kind.
func TestContractAddressCacheAcrossCallKinds(t *testing.T) {
	cfg := testParisCreateChainConfig()
	evm, ibs := newAuditEVM(t, cfg, Config{})
	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	a := types.HexToAddress("0xa000000000000000000000000000000000000001")
	b := types.HexToAddress("0xb000000000000000000000000000000000000002")
	for _, addr := range []types.Address{caller, a, b} {
		ibs.CreateAccount(addr, true)
	}
	slot := types.Hash{}
	ibs.SetState(a, &slot, *uint256.NewInt(7))
	ibs.SetState(b, &slot, *uint256.NewInt(9))
	// B: mstore(0, ADDRESS); mstore(32, sload(0)); return(0, 64)
	codeB := []byte{
		byte(ADDRESS), byte(PUSH1), 0, byte(MSTORE),
		byte(PUSH1), 0, byte(SLOAD), byte(PUSH1), 32, byte(MSTORE),
		byte(PUSH1), 64, byte(PUSH1), 0, byte(RETURN),
	}
	ibs.SetCode(b, codeB)
	// A: <callkind> B with gas, copy return data, return it. callkind from calldata[0].
	callFrame := func(kind OpCode) []byte {
		var c []byte
		if kind == CALL || kind == CALLCODE {
			c = append(c, byte(PUSH1), 64, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0) // retSize retOff argSize argOff value
		} else {
			c = append(c, byte(PUSH1), 64, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0)
		}
		c = append(c, byte(PUSH20))
		c = append(c, b[:]...)
		c = append(c, byte(GAS), byte(kind), byte(POP), byte(PUSH1), 64, byte(PUSH1), 0, byte(RETURN))
		return c
	}
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)
	for _, tc := range []struct {
		kind     OpCode
		wantAddr types.Address
		wantSlot uint64
	}{
		{CALL, b, 9}, {STATICCALL, b, 9}, {CALLCODE, a, 7}, {DELEGATECALL, a, 7},
	} {
		ibs.SetCode(a, callFrame(tc.kind))
		ret, _, err := evm.Call(AccountRef(caller), a, nil, 200000, uint256.NewInt(0), false)
		if err != nil || len(ret) != 64 {
			t.Fatalf("%s: ret %x err %v", tc.kind, ret, err)
		}
		if got := types.BytesToAddress(ret[12:32]); got != tc.wantAddr {
			t.Fatalf("%s: ADDRESS %s want %s", tc.kind, got, tc.wantAddr)
		}
		if got := new(uint256.Int).SetBytes(ret[32:64]).Uint64(); got != tc.wantSlot {
			t.Fatalf("%s: SLOAD %d want %d", tc.kind, got, tc.wantSlot)
		}
	}
	// CREATE: initcode returns ADDRESS as the deployed code.
	init := []byte{byte(ADDRESS), byte(PUSH1), 0, byte(MSTORE), byte(PUSH1), 20, byte(PUSH1), 12, byte(RETURN)}
	_, created, _, err := evm.Create(AccountRef(caller), init, 200000, uint256.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	if got := ibs.GetCode(created); !bytes.Equal(got, created[:]) {
		t.Fatalf("CREATE: deployed %x want %x", got, created[:])
	}
	// NewContract (public constructor) agrees with the EVM-owned frames.
	c := NewContract(AccountRef(caller), AccountRef(b), uint256.NewInt(0), 0, false)
	if c.Address() != b || c.addr != c.self.Address() {
		t.Fatal("NewContract did not cache the address")
	}
}
