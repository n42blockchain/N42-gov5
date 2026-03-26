package internal

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestCollectDepositExecutionRequestsIgnoresNonDepositLogs(t *testing.T) {
	t.Parallel()

	transferSig := types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	receipts := block.Receipts{
		&block.Receipt{
			Logs: []*block.Log{
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{transferSig},
					Data:    make([]byte, 32),
				},
			},
		},
	}

	requests, err := CollectDepositExecutionRequests(receipts)
	require.NoError(t, err)
	require.Nil(t, requests)
}

func TestCollectDepositExecutionRequestsExtractsDepositEventsAmongExtraLogs(t *testing.T) {
	t.Parallel()

	transferSig := types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	depositLogData := makeValidDepositLogData()
	expectedDeposit, err := vm.ParseDepositLog([]types.Hash{vm.DepositEventSignature}, depositLogData)
	require.NoError(t, err)

	receipts := block.Receipts{
		&block.Receipt{
			Logs: []*block.Log{
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{transferSig},
					Data:    make([]byte, 32),
				},
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{vm.DepositEventSignature},
					Data:    depositLogData,
				},
			},
		},
	}

	requests, err := CollectDepositExecutionRequests(receipts)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Len(t, requests[0], 1+vm.DepositRequestSize)
	require.Equal(t, byte(vm.DepositRequestType), requests[0][0])
	require.Equal(t, expectedDeposit.Serialize(), []byte(requests[0][1:]))
}

func makeValidDepositLogData() []byte {
	data := make([]byte, 832)
	offset := 160

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 48)
	offset += 32
	copy(data[offset:offset+48], bytes.Repeat([]byte{0xaa}, 48))
	offset += 48
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 32)
	offset += 32
	copy(data[offset:offset+32], bytes.Repeat([]byte{0xbb}, 32))
	offset += 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 8)
	offset += 32
	amount := uint64(32000000000)
	for i := 0; i < 8; i++ {
		data[offset+i] = byte(amount >> (8 * i))
	}
	offset += 8
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 96)
	offset += 32
	copy(data[offset:offset+96], bytes.Repeat([]byte{0xcc}, 96))
	offset += 96
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 8)
	offset += 32
	index := uint64(12345)
	for i := 0; i < 8; i++ {
		data[offset+i] = byte(index >> (8 * i))
	}

	return data
}

// testPragueConfig returns a ChainConfig with all forks through Prague activated at genesis.
func testPragueConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		Consensus:             params.Faker,
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

func TestProcessPragueSystemCallsSkipsUndeployedSystemContracts(t *testing.T) {
	t.Parallel()

	cfg := testPragueConfig()
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Time:       1,
		GasLimit:   30_000_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}

	t.Run("withdrawal", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		txDb := memdb.BeginRw(t, db)
		ibs := state.New(state.NewPlainState(txDb, 1))
		requests, err := ProcessPragueSystemCalls(cfg, ibs, header, nil)
		require.NoError(t, err)
		require.Empty(t, requests)
	})

	t.Run("consolidation", func(t *testing.T) {
		db := memdb.NewTestDB(t)
		txDb := memdb.BeginRw(t, db)
		ibs := state.New(state.NewPlainState(txDb, 1))
		ibs.CreateAccount(vm.WithdrawalRequestsAddress, true)
		ibs.SetCode(vm.WithdrawalRequestsAddress, []byte{byte(vm.STOP)})
		requests, err := ProcessPragueSystemCalls(cfg, ibs, header, nil)
		require.NoError(t, err)
		require.Empty(t, requests)
	})
}

func TestProcessPragueBlockStartStoresParentHashWhenHistoryContractExists(t *testing.T) {
	t.Parallel()

	cfg := testPragueConfig()
	parentHash := types.HexToHash("0x1234")
	header := &block.Header{
		ParentHash: parentHash,
		Number:     uint256.NewInt(2),
		Time:       1,
		GasLimit:   30_000_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}

	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))
	ibs.CreateAccount(vm.HistoryStorageAddress, true)
	ibs.SetCode(vm.HistoryStorageAddress, vm.HistoryStorageCode)

	require.NoError(t, ProcessPragueBlockStart(cfg, ibs, header))

	slot := types.Hash{}
	uint256.NewInt(1).WriteToSlice(slot[:])
	var got uint256.Int
	ibs.GetState(vm.HistoryStorageAddress, &slot, &got)
	stored := types.Hash{}
	got.WriteToSlice(stored[:])
	require.Equal(t, parentHash, stored)
}

func TestProcessPragueBlockStartStoresGenesisHashAtSlotZeroWhenHistoryContractExists(t *testing.T) {
	t.Parallel()

	cfg := testPragueConfig()
	parentHash := types.HexToHash("0xabcd")
	header := &block.Header{
		ParentHash: parentHash,
		Number:     uint256.NewInt(1),
		Time:       1,
		GasLimit:   30_000_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}

	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))
	ibs.CreateAccount(vm.HistoryStorageAddress, true)
	ibs.SetCode(vm.HistoryStorageAddress, vm.HistoryStorageCode)

	require.NoError(t, ProcessPragueBlockStart(cfg, ibs, header))

	slot := types.Hash{}
	var got uint256.Int
	ibs.GetState(vm.HistoryStorageAddress, &slot, &got)
	stored := types.Hash{}
	got.WriteToSlice(stored[:])
	require.Equal(t, parentHash, stored)
}

func TestProcessPragueBlockStartNoOpPrePrague(t *testing.T) {
	t.Parallel()
	cfg := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		Consensus:   params.Faker,
		LondonBlock: big.NewInt(0),
	}
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Time:       1,
		Difficulty: uint256.NewInt(0),
	}
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))
	require.NoError(t, ProcessPragueBlockStart(cfg, ibs, header))
	// History contract should NOT be deployed for pre-Prague blocks.
	require.Equal(t, 0, ibs.GetCodeSize(vm.HistoryStorageAddress))
}

func TestProcessPragueBlockStartNoOpWhenHistoryContractMissing(t *testing.T) {
	t.Parallel()
	cfg := testPragueConfig()
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Time:       1,
		GasLimit:   30_000_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))
	require.NoError(t, ProcessPragueBlockStart(cfg, ibs, header))
	require.Equal(t, 0, ibs.GetCodeSize(vm.HistoryStorageAddress))
	slot := types.Hash{}
	var got uint256.Int
	ibs.GetState(vm.HistoryStorageAddress, &slot, &got)
	require.True(t, got.IsZero())
}
