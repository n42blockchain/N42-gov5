package vm

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// g65FusakaPrecompileRegistry records precompile registrations made by
// RegisterFusakaPrecompiles so the test can assert on them without pulling
// in the full production precompile registry.
type g65FusakaPrecompileRegistry struct {
	registered map[types.Address]PrecompiledContract
}

func (r *g65FusakaPrecompileRegistry) Register(addr types.Address, contract PrecompiledContract) {
	if r.registered == nil {
		r.registered = make(map[types.Address]PrecompiledContract)
	}
	r.registered[addr] = contract
}

func TestRegisterFusakaPrecompilesRegistersP256Verify(t *testing.T) {
	t.Parallel()

	reg := &g65FusakaPrecompileRegistry{}
	RegisterFusakaPrecompiles(reg)

	contract, ok := reg.registered[P256VerifyAddress]
	if !ok {
		t.Fatalf("expected RegisterFusakaPrecompiles to register %s", P256VerifyAddress)
	}
	if contract == nil {
		t.Fatalf("registered P256Verify contract must not be nil")
	}
}

func TestEnable7907And7951AreNoOpsOnTheJumpTable(t *testing.T) {
	t.Parallel()

	jt := newOsakaInstructionSet()
	before := jt

	enable7907(&jt)
	enable7951(&jt)

	for i := 0; i < len(jt); i++ {
		if jt[i] != before[i] {
			t.Fatalf("opcode %#x changed, but EIP-7907/7951 only add precompiles, not opcodes", i)
		}
	}
}

func TestEnable7702And2537AreNoOpsOnTheJumpTable(t *testing.T) {
	t.Parallel()

	jt := newPragueInstructionSet()
	before := jt

	enable7702(&jt)
	enable2537(&jt)

	for i := 0; i < len(jt); i++ {
		if jt[i] != before[i] {
			t.Fatalf("opcode %#x changed, but EIP-7702/2537 only affect call semantics and precompiles", i)
		}
	}
}

// g65FakeHistoryStateDB is a minimal in-memory StateDB sufficient to drive
// StoreParentBlockHash and EnsureHistoryContractDeployed.
type g65FakeHistoryStateDB struct {
	state map[types.Address]map[types.Hash]uint256.Int
	code  map[types.Address][]byte
}

func newG65FakeHistoryStateDB() *g65FakeHistoryStateDB {
	return &g65FakeHistoryStateDB{
		state: make(map[types.Address]map[types.Hash]uint256.Int),
		code:  make(map[types.Address][]byte),
	}
}

func (s *g65FakeHistoryStateDB) GetState(addr types.Address, key *types.Hash, value *uint256.Int) {
	if m, ok := s.state[addr]; ok {
		if v, ok := m[*key]; ok {
			value.Set(&v)
			return
		}
	}
	value.Clear()
}

func (s *g65FakeHistoryStateDB) SetState(addr types.Address, key *types.Hash, value uint256.Int) {
	m, ok := s.state[addr]
	if !ok {
		m = make(map[types.Hash]uint256.Int)
		s.state[addr] = m
	}
	m[*key] = value
}

func (s *g65FakeHistoryStateDB) GetCode(addr types.Address) []byte {
	return s.code[addr]
}

func (s *g65FakeHistoryStateDB) SetCode(addr types.Address, code []byte) {
	s.code[addr] = code
}

func TestStoreParentBlockHashWritesExpectedSlot(t *testing.T) {
	t.Parallel()

	db := newG65FakeHistoryStateDB()
	parentHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111")

	StoreParentBlockHash(db, 5, parentHash)

	wantSlot := types.Hash{}
	slotNum := new(uint256.Int).SetUint64(5 % HistoryServeWindow)
	slotNum.WriteToSlice(wantSlot[:])

	var got uint256.Int
	db.GetState(HistoryStorageAddress, &wantSlot, &got)

	want := new(uint256.Int).SetBytes(parentHash[:])
	if !got.Eq(want) {
		t.Fatalf("stored hash = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestStoreParentBlockHashWrapsAroundHistoryServeWindow(t *testing.T) {
	t.Parallel()

	db := newG65FakeHistoryStateDB()
	parentHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222")

	// parentNumber and parentNumber+HistoryServeWindow must land on the same slot.
	StoreParentBlockHash(db, 3, parentHash)

	slotA := types.Hash{}
	new(uint256.Int).SetUint64(3 % HistoryServeWindow).WriteToSlice(slotA[:])
	slotB := types.Hash{}
	new(uint256.Int).SetUint64((3 + HistoryServeWindow) % HistoryServeWindow).WriteToSlice(slotB[:])

	if slotA != slotB {
		t.Fatalf("expected congruent block numbers to map to the same slot")
	}
}

func TestEnsureHistoryContractDeployedIsIdempotent(t *testing.T) {
	t.Parallel()

	db := newG65FakeHistoryStateDB()
	if len(db.GetCode(HistoryStorageAddress)) != 0 {
		t.Fatalf("expected no code before deployment")
	}

	EnsureHistoryContractDeployed(db)
	first := db.GetCode(HistoryStorageAddress)
	if len(first) == 0 {
		t.Fatalf("expected the history contract code to be deployed")
	}

	// A second call with different pre-existing code must be a no-op: it
	// only deploys when the account currently has no code at all.
	db.SetCode(HistoryStorageAddress, append([]byte{}, first...))
	EnsureHistoryContractDeployed(db)
	if string(db.GetCode(HistoryStorageAddress)) != string(first) {
		t.Fatalf("EnsureHistoryContractDeployed must not redeploy once code exists")
	}
}

func TestUserOperationGetPaymasterData(t *testing.T) {
	t.Parallel()

	paymaster := types.HexToAddress("0x0000000000000000000000000000000000000042")
	extra := []byte{0xAA, 0xBB, 0xCC}

	withData := &UserOperation{PaymasterAndData: append(paymaster.Bytes(), extra...)}
	if got := withData.GetPaymasterData(); string(got) != string(extra) {
		t.Fatalf("GetPaymasterData() = %x, want %x", got, extra)
	}

	exact := &UserOperation{PaymasterAndData: paymaster.Bytes()}
	if got := exact.GetPaymasterData(); got != nil {
		t.Fatalf("GetPaymasterData() with no extra data = %x, want nil", got)
	}

	empty := &UserOperation{}
	if got := empty.GetPaymasterData(); got != nil {
		t.Fatalf("GetPaymasterData() with empty PaymasterAndData = %x, want nil", got)
	}
}
