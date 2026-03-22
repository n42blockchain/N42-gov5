package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
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
