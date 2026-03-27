package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func testACLChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
		PragueTime:            big.NewInt(0),
		PectraTime:            big.NewInt(0),
	}
}

func testPragueOnlyACLChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
		PragueTime:            big.NewInt(0),
	}
}

func newTestACLEVM(t *testing.T) (*EVM, *state.IntraBlockState) {
	t.Helper()

	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(evmtypes.IntraBlockState, types.Address, *uint256.Int) bool { return true },
		Transfer:    func(evmtypes.IntraBlockState, types.Address, types.Address, *uint256.Int, bool) {},
		GetHash:     func(uint64) types.Hash { return types.Hash{} },
		Coinbase:    types.Address{},
		GasLimit:    1_000_000,
		BlockNumber: 1,
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     uint256.NewInt(0),
	}
	evm := NewEVM(blockCtx, evmtypes.TxContext{}, ibs, testACLChainConfig(), Config{})
	return evm, ibs
}

func newTestACLEVMWithConfig(t *testing.T, cfg *params.ChainConfig) (*EVM, *state.IntraBlockState) {
	t.Helper()

	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(evmtypes.IntraBlockState, types.Address, *uint256.Int) bool { return true },
		Transfer:    func(evmtypes.IntraBlockState, types.Address, types.Address, *uint256.Int, bool) {},
		GetHash:     func(uint64) types.Hash { return types.Hash{} },
		Coinbase:    types.Address{},
		GasLimit:    1_000_000,
		BlockNumber: 1,
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     uint256.NewInt(0),
	}
	evm := NewEVM(blockCtx, evmtypes.TxContext{}, ibs, cfg, Config{})
	return evm, ibs
}

func newCallGasStack(addr types.Address) *stack.Stack {
	s := stack.New()
	s.Push(uint256.NewInt(0))
	s.Push(new(uint256.Int).SetBytes(addr.Bytes()))
	s.Push(uint256.NewInt(0))
	return s
}

func TestGasCallEIP2929ChargesColdDelegatedTargetAccess(t *testing.T) {
	evm, ibs := newTestACLEVM(t)

	caller := types.HexToAddress("0x1000000000000000000000000000000000000001")
	authority := types.HexToAddress("0x2000000000000000000000000000000000000002")
	target := types.HexToAddress("0x3000000000000000000000000000000000000003")

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(authority, true)
	ibs.SetCode(authority, AddressToDelegation(target))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, []byte{byte(STOP)})

	contract := NewContract(AccountRef(caller), AccountRef(caller), uint256.NewInt(0), 1_000_000, false)
	gas, err := gasCallEIP2929(evm, contract, newCallGasStack(authority), NewMemory(), 0)
	if err != nil {
		t.Fatalf("gasCallEIP2929 error: %v", err)
	}
	if want := (params.ColdAccountAccessCostEIP2929 - params.WarmStorageReadCostEIP2929) + params.ColdAccountAccessCostEIP2929; gas != want {
		t.Fatalf("gasCallEIP2929 = %d, want %d", gas, want)
	}
	if !ibs.AddressInAccessList(authority) {
		t.Fatal("authority should be warmed after CALL gas calculation")
	}
	if !ibs.AddressInAccessList(target) {
		t.Fatal("delegated target should be warmed after CALL gas calculation")
	}
}

func TestGasCallEIP2929ChargesWarmDelegatedTargetAccess(t *testing.T) {
	evm, ibs := newTestACLEVM(t)

	caller := types.HexToAddress("0x4000000000000000000000000000000000000004")
	authority := types.HexToAddress("0x5000000000000000000000000000000000000005")
	target := types.HexToAddress("0x6000000000000000000000000000000000000006")

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(authority, true)
	ibs.SetCode(authority, AddressToDelegation(target))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, []byte{byte(STOP)})
	ibs.AddAddressToAccessList(target)

	contract := NewContract(AccountRef(caller), AccountRef(caller), uint256.NewInt(0), 1_000_000, false)
	gas, err := gasCallEIP2929(evm, contract, newCallGasStack(authority), NewMemory(), 0)
	if err != nil {
		t.Fatalf("gasCallEIP2929 error: %v", err)
	}
	if want := (params.ColdAccountAccessCostEIP2929 - params.WarmStorageReadCostEIP2929) + params.WarmStorageReadCostEIP2929; gas != want {
		t.Fatalf("gasCallEIP2929 = %d, want %d", gas, want)
	}
}

func TestGasCallEIP2929ChargesWarmSelfDelegationAccess(t *testing.T) {
	evm, ibs := newTestACLEVM(t)

	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	authority := crypto.CreateAddress(caller, 1)

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(authority, true)
	ibs.SetCode(authority, AddressToDelegation(authority))

	contract := NewContract(AccountRef(caller), AccountRef(caller), uint256.NewInt(0), 1_000_000, false)
	gas, err := gasCallEIP2929(evm, contract, newCallGasStack(authority), NewMemory(), 0)
	if err != nil {
		t.Fatalf("gasCallEIP2929 error: %v", err)
	}
	if want := (params.ColdAccountAccessCostEIP2929 - params.WarmStorageReadCostEIP2929) + params.WarmStorageReadCostEIP2929; gas != want {
		t.Fatalf("gasCallEIP2929 = %d, want %d", gas, want)
	}
}

func TestGasCallEIP2929ChargesDelegatedTargetAccessFromPrague(t *testing.T) {
	evm, ibs := newTestACLEVMWithConfig(t, testPragueOnlyACLChainConfig())

	caller := types.HexToAddress("0x8000000000000000000000000000000000000008")
	authority := types.HexToAddress("0x9000000000000000000000000000000000000009")
	target := types.HexToAddress("0xa00000000000000000000000000000000000000a")

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(authority, true)
	ibs.SetCode(authority, AddressToDelegation(target))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, []byte{byte(STOP)})

	contract := NewContract(AccountRef(caller), AccountRef(caller), uint256.NewInt(0), 1_000_000, false)
	gas, err := gasCallEIP2929(evm, contract, newCallGasStack(authority), NewMemory(), 0)
	if err != nil {
		t.Fatalf("gasCallEIP2929 error: %v", err)
	}
	if want := (params.ColdAccountAccessCostEIP2929 - params.WarmStorageReadCostEIP2929) + params.ColdAccountAccessCostEIP2929; gas != want {
		t.Fatalf("gasCallEIP2929 = %d, want %d", gas, want)
	}
	if !ibs.AddressInAccessList(target) {
		t.Fatal("delegated target should be warmed during Prague CALL gas calculation")
	}
}
