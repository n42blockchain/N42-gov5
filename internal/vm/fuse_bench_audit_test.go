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

// benchLoopCode counts down from n with the PUSH+JUMPI pattern the fusion
// targets: JUMPDEST PUSH1 1 SWAP1 SUB DUP1 PUSH1 <dest> JUMPI STOP.
func benchLoopCode(n uint16) []byte {
	return []byte{
		byte(PUSH2), byte(n >> 8), byte(n), // 0
		byte(JUMPDEST),                                     // 3
		byte(PUSH1), 1, byte(SWAP1), byte(SUB), byte(DUP1), // 4
		byte(PUSH1), 3, byte(JUMPI), // 9
		byte(STOP), // 12
	}
}

func benchEVM(b *testing.B, cfg Config) (*EVM, *state.IntraBlockState) {
	b.Helper()
	db := memdb.NewTestDB(b)
	tx := memdb.BeginRw(b, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	chainCfg := &params.ChainConfig{
		ChainID: big.NewInt(1), HomesteadBlock: big.NewInt(0), TangerineWhistleBlock: big.NewInt(0), SpuriousDragonBlock: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0), MergeNetsplitBlock: big.NewInt(0),
	}
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(evmtypes.IntraBlockState, types.Address, *uint256.Int) bool { return true },
		Transfer:    func(evmtypes.IntraBlockState, types.Address, types.Address, *uint256.Int, bool) {},
		GetHash:     func(uint64) types.Hash { return types.Hash{} },
		GasLimit:    30_000_000, BlockNumber: 1, Time: 1, Difficulty: big.NewInt(0), BaseFee: uint256.NewInt(0),
	}
	return NewEVM(blockCtx, evmtypes.TxContext{}, ibs, chainCfg, cfg), ibs
}

func benchLoop(b *testing.B, cache *CodeAnalysisCache) {
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = cache
	defer func() { GlobalCodeAnalysisCache = saved }()
	evm, ibs := benchEVM(b, Config{})
	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	target := types.HexToAddress("0x8000000000000000000000000000000000000008")
	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, benchLoopCode(10000))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := evm.Call(AccountRef(caller), target, nil, 10_000_000, uint256.NewInt(0), false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoopFused(b *testing.B) { benchLoop(b, NewCodeAnalysisCache(16)) }
func BenchmarkLoopPlain(b *testing.B) { benchLoop(b, nil) }
