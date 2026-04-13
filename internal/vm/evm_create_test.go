package vm

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func testParisCreateChainConfig() *params.ChainConfig {
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
		MergeNetsplitBlock:    big.NewInt(0),
	}
}

func testLondonCreateChainConfig() *params.ChainConfig {
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
	}
}

func newCreateTestEVM(t *testing.T, cfg *params.ChainConfig) (*EVM, *state.IntraBlockState) {
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
	return NewEVM(blockCtx, evmtypes.TxContext{}, ibs, cfg, Config{}), ibs
}

func TestCreateRejectsNonEmptyStorageCollisionFromParis(t *testing.T) {
	evm, ibs := newCreateTestEVM(t, testParisCreateChainConfig())

	caller := types.HexToAddress("0x1000000000000000000000000000000000000001")
	created := types.HexToAddress("0x2000000000000000000000000000000000000002")
	key := types.Hash{31: 1}

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(created, true)
	ibs.SetState(created, &key, *uint256.NewInt(1))

	_, _, leftOverGas, err := evm.create(AccountRef(caller), &codeAndHash{code: []byte{byte(STOP)}}, 100_000, uint256.NewInt(0), created, CREATE, false)
	if err != ErrContractAddressCollision {
		t.Fatalf("create error = %v, want %v", err, ErrContractAddressCollision)
	}
	if leftOverGas != 0 {
		t.Fatalf("leftOverGas = %d, want 0", leftOverGas)
	}
}

func TestCreateAllowsStorageOnlyCollisionPreParis(t *testing.T) {
	evm, ibs := newCreateTestEVM(t, testLondonCreateChainConfig())

	caller := types.HexToAddress("0x3000000000000000000000000000000000000003")
	created := types.HexToAddress("0x4000000000000000000000000000000000000004")
	key := types.Hash{31: 1}

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(created, true)
	ibs.SetState(created, &key, *uint256.NewInt(1))

	_, _, _, err := evm.create(AccountRef(caller), &codeAndHash{code: []byte{byte(STOP)}}, 100_000, uint256.NewInt(0), created, CREATE, false)
	if err == ErrContractAddressCollision {
		t.Fatalf("create unexpectedly returned %v before Paris", err)
	}
}

func TestHasNonEmptyStorageClearedAfterSelfdestructAndBalanceOnlyRecreation(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	cfg := testParisCreateChainConfig()
	rules := cfg.RulesWithTimestamp(1, 1)

	caller := types.HexToAddress("0x5000000000000000000000000000000000000005")
	created := types.HexToAddress("0x6000000000000000000000000000000000000006")
	slot0 := types.Hash{}

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer1 := state.NewPlainStateWriter(tx, tx, 1)
		sdb1 := state.New(state.NewPlainState(tx, 1))
		sdb1.CreateAccount(caller, false)
		sdb1.CreateAccount(created, true)
		sdb1.SetCode(created, []byte{byte(STOP)})
		sdb1.SetState(created, &slot0, *uint256.NewInt(1))
		if err := sdb1.FinalizeTx(rules, writer1); err != nil {
			return err
		}
		if err := sdb1.CommitBlock(rules, writer1); err != nil {
			return err
		}

		writer2 := state.NewPlainStateWriter(tx, tx, 2)
		sdb2 := state.New(state.NewPlainState(tx, 2))
		sdb2.Selfdestruct(created)
		if err := sdb2.FinalizeTx(rules, writer2); err != nil {
			return err
		}
		sdb2.AddBalance(created, uint256.NewInt(1))
		if err := sdb2.FinalizeTx(rules, writer2); err != nil {
			return err
		}
		if err := sdb2.CommitBlock(rules, writer2); err != nil {
			return err
		}

		sdb3 := state.New(state.NewPlainState(tx, 3))
		if sdb3.HasNonEmptyStorage(created) {
			t.Fatalf("HasNonEmptyStorage(%s) = true, want false after selfdestruct + balance-only recreation", created)
		}
		if nonce := sdb3.GetNonce(created); nonce != 0 {
			t.Fatalf("nonce = %d, want 0 for balance-only account", nonce)
		}
		if codeHash := sdb3.GetCodeHash(created); !account.IsEmptyCodeHash(codeHash) {
			t.Fatalf("codeHash = %x, want empty code hash for balance-only account", codeHash)
		}
		blockCtx := evmtypes.BlockContext{
			CanTransfer: func(evmtypes.IntraBlockState, types.Address, *uint256.Int) bool { return true },
			Transfer:    func(evmtypes.IntraBlockState, types.Address, types.Address, *uint256.Int, bool) {},
			GetHash:     func(uint64) types.Hash { return types.Hash{} },
			Coinbase:    types.Address{},
			GasLimit:    1_000_000,
			BlockNumber: 3,
			Time:        1,
			Difficulty:  big.NewInt(0),
			BaseFee:     uint256.NewInt(0),
		}
		evm := NewEVM(blockCtx, evmtypes.TxContext{}, sdb3, cfg, Config{})
		if _, _, leftOverGas, err := evm.create(AccountRef(caller), &codeAndHash{code: []byte{byte(STOP)}}, 100_000, uint256.NewInt(0), created, CREATE, false); err != nil {
			t.Fatalf("create on balance-only account returned %v", err)
		} else if leftOverGas == 0 {
			t.Fatal("create on balance-only account consumed all gas unexpectedly")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatedContractSecondCallAfterSelfdestructToSelfInSameTxPreCancun(t *testing.T) {
	evm, ibs := newCreateTestEVM(t, testParisCreateChainConfig())

	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	created := types.HexToAddress("0x8000000000000000000000000000000000000008")

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(created, true)
	ibs.SetCode(created, buildSelfdestructBalanceBugContract())
	ibs.SetBalance(created, uint256.NewInt(3))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	if _, _, err := evm.Call(AccountRef(caller), created, nil, 100_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("first call returned %v", err)
	}
	if got := ibs.GetBalance(created).Uint64(); got != 0 {
		t.Fatalf("balance after first selfdestruct call = %d, want 0", got)
	}

	if _, _, err := evm.Call(AccountRef(caller), created, nil, 100_000, uint256.NewInt(1), false); err != nil {
		t.Fatalf("second call returned %v", err)
	}
	if got := ibs.GetBalance(created).Uint64(); got != 0 {
		t.Fatalf("balance after second call = %d, want 0", got)
	}
}

func TestCreatedContractStorageAccessibleAfterSelfdestructSameTxPreCancun(t *testing.T) {
	evm, ibs := newCreateTestEVM(t, testParisCreateChainConfig())

	caller := types.HexToAddress("0x9000000000000000000000000000000000000009")
	created := types.HexToAddress("0xa00000000000000000000000000000000000000a")
	beneficiary := types.HexToAddress("0xb00000000000000000000000000000000000000b")
	slot1 := types.Hash{31: 1}

	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(created, true)
	ibs.SetCode(created, buildSelfdestructStoreContract(beneficiary))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	if _, _, err := evm.Call(AccountRef(caller), created, encodeSingleWordInput(2), 150_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("first storage call returned %v", err)
	}
	if _, _, err := evm.Call(AccountRef(caller), created, encodeSingleWordInput(1), 150_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("selfdestruct call returned %v", err)
	}
	if _, _, err := evm.Call(AccountRef(caller), created, encodeSingleWordInput(2), 150_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("second storage call returned %v", err)
	}

	ret, _, err := evm.Call(AccountRef(caller), created, encodeSingleWordInput(3), 150_000, uint256.NewInt(0), false)
	if err != nil {
		t.Fatalf("readback call returned %v", err)
	}
	if len(ret) != 32 || ret[31] != 0x24 {
		t.Fatalf("returned storage = %x, want ...24", ret)
	}

	var stored uint256.Int
	ibs.GetState(created, &slot1, &stored)
	if got := stored.Uint64(); got != 0x24 {
		t.Fatalf("slot1 = %d, want %d", got, 0x24)
	}
}

func buildSelfdestructBalanceBugContract() []byte {
	// If calldata word == 1, store SELFBALANCE in slot 0.
	// Otherwise SELFDESTRUCT to self.
	return []byte{
		byte(PUSH1), 0x00,
		byte(CALLDATALOAD),
		byte(PUSH1), 0x01,
		byte(EQ),
		byte(PUSH1), 0x0b,
		byte(JUMPI),
		byte(ADDRESS),
		byte(SELFDESTRUCT),
		byte(JUMPDEST),
		byte(SELFBALANCE),
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
}

func buildSelfdestructStoreContract(beneficiary types.Address) []byte {
	code := []byte{
		byte(PUSH1), 0x00,
		byte(CALLDATALOAD),
		byte(DUP1),
		byte(PUSH1), 0x01,
		byte(EQ),
		byte(PUSH1), 0x00, // selfdestruct label
		byte(JUMPI),
		byte(DUP1),
		byte(PUSH1), 0x02,
		byte(EQ),
		byte(PUSH1), 0x00, // add-storage label
		byte(JUMPI),
		byte(PUSH1), 0x03,
		byte(EQ),
		byte(PUSH1), 0x00, // get-storage label
		byte(JUMPI),
		byte(STOP),
		byte(JUMPDEST),
		byte(PUSH20),
	}
	code = append(code, beneficiary.Bytes()...)
	code = append(code,
		byte(SELFDESTRUCT),
		byte(JUMPDEST),
		byte(PUSH1), 0x01,
		byte(SLOAD),
		byte(PUSH1), 0x12,
		byte(ADD),
		byte(PUSH1), 0x01,
		byte(SSTORE),
		byte(STOP),
		byte(JUMPDEST),
		byte(PUSH1), 0x01,
		byte(SLOAD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)

	code[8] = 24
	code[15] = 47
	code[21] = 58
	return code
}

func encodeSingleWordInput(v byte) []byte {
	input := make([]byte, 32)
	input[31] = v
	return input
}
