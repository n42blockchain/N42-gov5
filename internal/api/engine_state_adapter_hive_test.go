package api

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ethel"
	vmcore "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	logv3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestHiveEngineGenesisFixtureMatchesCurrentGethRoot(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	genesisPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "tests", "eth-hive", "simulators", "ethereum", "engine", "init", "genesis.json")
	genesisJSON, err := os.ReadFile(genesisPath)
	require.NoError(t, err)

	var genesis conf.Genesis
	require.NoError(t, json.Unmarshal(genesisJSON, &genesis))

	const (
		expectedStateRoot = "0x84308e7d0abf860412f4a0c6fc25709a6e9eaba20a0d085a0344d271f40109ec"
		expectedHash      = "0x67ead97eb79b47a1638659942384143f36ed44275d4182799875ab5a87324055"
	)

	db := memdb.NewTestDB(t)
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: &genesis}).WriteGenesisState(tx)
		require.NoError(t, err)

		root, err := ethel.VerifyStateRoot(tx)
		require.NoError(t, err)
		require.Equal(t, types.HexToHash(expectedStateRoot), root)
		require.Equal(t, types.HexToHash(expectedStateRoot), blk.StateRoot())
		require.Equal(t, types.HexToHash(expectedHash), blk.Hash())
		return nil
	})
	require.NoError(t, err)
}

func TestHiveEnginePlainStateAtBlockOnePreservesGenesisRoot(t *testing.T) {
	db, _, genesisBlock := newHiveEngineGenesisDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		root, err := ethel.VerifyStateRoot(tx)
		require.NoError(t, err)
		require.Equal(t, genesisBlock.StateRoot(), root)

		reader := state.NewPlainState(tx, 1)
		ibs := state.New(reader)
		ethel.SetupStateRootComputer(tx, ibs)
		require.NoError(t, ethel.InitHashState(tx))
		require.Equal(t, genesisBlock.StateRoot(), ibs.IntermediateRoot())
		return nil
	})
	require.NoError(t, err)
}

func TestHiveEngineBuildsFirstEmptyPayloadWithGenesisStateRoot(t *testing.T) {
	db, genesis, genesisBlock := newHiveEngineGenesisDB(t)

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: genesis.Config,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}},
		chainConfig:   genesis.Config,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: ethCompatibleBlockHash(genesisBlock, genesis.Config),
	}, &PayloadAttributesV1{
		Timestamp:             hexutil.Uint64(0x1235),
		PrevRandao:            types.HexToHash("0x1a6da4e66eed7d5e89e8fb8ce0cff225c8adebc0af569fd8533a1107cc8d7f8b"),
		SuggestedFeeRecipient: types.Address{},
	})
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.Equal(t, genesisBlock.StateRoot(), payload.StateRoot)
}

func TestHiveEngineStateAdapterAcceptsFirstEmptyCanonicalPayload(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)

	payload := &ExecutionPayloadV1{
		ParentHash:    types.HexToHash("0xd462b6793c2895fd61cd63e098874774d3e03556e14ec40fb9861956e39eb8b0"),
		FeeRecipient:  types.Address{},
		StateRoot:     types.HexToHash("0xdac58864b1d70d65174a6ead9c462edc165ca9f05e32778b64eb7059a789ec74"),
		ReceiptsRoot:  types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.HexToHash("0x1a6da4e66eed7d5e89e8fb8ce0cff225c8adebc0af569fd8533a1107cc8d7f8b"),
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(0x2ffbd2),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(0x1235),
		ExtraData:     types.Hex2Bytes("d883011007846765746888676f312e32352e38856c696e7578"),
		BaseFeePerGas: hexutil.Uint64(0x342770c0),
		BlockHash:     types.HexToHash("0xcac5f6605258df666b76d2d38e9549be9aae0a44dc08493888d21b23f3687ef3"),
		Transactions:  []hexutil.Bytes{},
	}
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(blk, genesis.Config))

	valid, stateRoot, err := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).ExecutePayload(blk.(*block.Block))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, payload.StateRoot, stateRoot)
}

func TestHiveEngineStateAdapterAcceptsFirstEmptyCanonicalPayloadAfterMDBXReopen(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisMDBXDB(t)
	defer db.Close()

	payload := &ExecutionPayloadV1{
		ParentHash:    types.HexToHash("0xd462b6793c2895fd61cd63e098874774d3e03556e14ec40fb9861956e39eb8b0"),
		FeeRecipient:  types.Address{},
		StateRoot:     types.HexToHash("0xdac58864b1d70d65174a6ead9c462edc165ca9f05e32778b64eb7059a789ec74"),
		ReceiptsRoot:  types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.HexToHash("0x1a6da4e66eed7d5e89e8fb8ce0cff225c8adebc0af569fd8533a1107cc8d7f8b"),
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(0x2ffbd2),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(0x1235),
		ExtraData:     types.Hex2Bytes("d883011007846765746888676f312e32352e38856c696e7578"),
		BaseFeePerGas: hexutil.Uint64(0x342770c0),
		BlockHash:     types.HexToHash("0xcac5f6605258df666b76d2d38e9549be9aae0a44dc08493888d21b23f3687ef3"),
		Transactions:  []hexutil.Bytes{},
	}
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(blk, genesis.Config))

	valid, stateRoot, err := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).ExecutePayload(blk.(*block.Block))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, payload.StateRoot, stateRoot)
}

func TestHiveEngineNewPayloadRejectsMismatchedFirstStateRoot(t *testing.T) {
	db, genesis, genesisBlock := newHiveEngineGenesisDB(t)

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: genesis.Config,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   genesis.Config,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	payload := hiveFirstEmptyPayload()
	payload.StateRoot = types.HexToHash("0xdac58864b1d70d65174a6ead9c462edc165ca9f05e32778b64eb7059a789ec75")
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleBlockHash(blk, genesis.Config)

	resp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.ParentHash, *resp.LatestValidHash)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "state root mismatch", *resp.ValidationError)
}

func TestHiveEngineNewPayloadRejectsMismatchedFirstReceiptsRoot(t *testing.T) {
	db, genesis, genesisBlock := newHiveEngineGenesisDB(t)

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: genesis.Config,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   genesis.Config,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	payload := hiveFirstEmptyPayload()
	payload.ReceiptsRoot = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b422")
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleBlockHash(blk, genesis.Config)

	resp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.ParentHash, *resp.LatestValidHash)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "receipts root mismatch", *resp.ValidationError)
}

func TestStablePragueModexpCallcodeFixtureTouchesExpectedExecutionState(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	cfg := &params.ChainConfig{
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

	beaconRootsAddr := params.BeaconRootsAddress
	historyAddr := vmcore.HistoryStorageAddress
	withdrawalQueueAddr := vmcore.WithdrawalRequestsAddress
	consolidationQueueAddr := vmcore.ConsolidationRequestsAddress
	modexpReaderAddr := types.HexToAddress("0x2af898aa328a87cfb23fbaf9302887d7c37df8fe")
	sender := types.HexToAddress("0xe88c5d94c986f78708f74a3ed89b9ea177cbb8e5")
	feeRecipient := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	uint64Ptr := func(v uint64) *uint64 { return &v }

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				beaconRootsAddr: {
					Balance: "0x0",
					Nonce:   1,
					Code:    params.BeaconRootsCode,
				},
				historyAddr: {
					Balance: "0x0",
					Nonce:   1,
					Code:    vmcore.HistoryStorageCode,
				},
				withdrawalQueueAddr: {
					Balance: "0x0",
					Nonce:   1,
					Code:    vmcore.WithdrawalRequestQueueCode,
				},
				consolidationQueueAddr: {
					Balance: "0x0",
					Nonce:   1,
					Code:    vmcore.ConsolidationRequestQueueCode,
				},
				modexpReaderAddr: {
					Balance: "0x0",
					Nonce:   1,
					Code:    types.Hex2Bytes("3660006000375a600060003660006000600560c8f25a906000553d60015561007a0190036002553d600060003e3d600020600355"),
				},
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
			},
			Number:     0,
			GasLimit:   0x07270e00,
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   types.Address{},
		},
	}

	db := memdb.NewTestDB(t)
	var genesisBlock *block.Block
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	rawTx := hexutil.MustDecode("0x02f9011801808007830238a0942af898aa328a87cfb23fbaf9302887d7c37df8fe80b8b4000000000000000000000000000000000000000000000000000000000000002800000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000028e8e77626586f73b955364c7b4bbf0bb7f7685ebd40e852b164633a4acbd3244c000102030405060701fffffff01681d2220bfea4bb888a5543db8c0916274ddb1ea93b144c042c01d8164c950001020304050607c080a0efac5255b8dc509c4604cf399f583589203c5f2b2ed59d0cbe7fd14e1866a8eda07c0b0f0c7f988e91c064f0290249de70ca5e0f0ce0833987844ed38abd46a55c")
	tx, err := transaction.DecodeEthereumTransaction(rawTx)
	require.NoError(t, err)

	header := &block.Header{
		ParentHash:       genesisBlock.Hash(),
		Number:           uint256.NewInt(1),
		GasLimit:         0x07270e00,
		Time:             0x3e8,
		BaseFee:          uint256.NewInt(7),
		Difficulty:       uint256.NewInt(0),
		Coinbase:         feeRecipient,
		BlobGasUsed:      uint64Ptr(0),
		ExcessBlobGas:    uint64Ptr(0),
		ParentBeaconRoot: ptrToHash(types.Hash{}),
	}
	var (
		expectedRoot         types.Hash
		expectedReceiptsRoot types.Hash
		expectedBloom        block.Bloom
		verifiedRoot         types.Hash
		fullVerifiedRoot     types.Hash
	)

	err = db.Update(context.Background(), func(txdb kv.RwTx) error {
		reader := state.NewPlainState(txdb, 1)
		ibs := state.New(reader)
		ethel.SetupStateRootComputer(txdb, ibs)

		require.NoError(t, internalcore.ProcessExecutionBlockStart(header.ParentBeaconRoot, cfg, ibs, header, &apiTestEngine{}))

		gasPool := new(common.GasPool).AddGas(header.GasLimit)
		var usedGas uint64
		receipt, _, err := internalcore.ApplyTransaction(
			cfg,
			func(uint64) types.Hash { return types.Hash{} },
			&apiTestEngine{},
			nil,
			gasPool,
			ibs,
			state.NewNoopWriter(),
			header,
			tx,
			&usedGas,
			vmcore.Config{},
		)
		require.NoError(t, err)
		require.NotNil(t, receipt)
		require.Equal(t, uint64(0x1b3f4), usedGas)

		_, err = internalcore.ProcessExecutionBlockEnd(block.Receipts{receipt}, cfg, ibs, header, &apiTestEngine{})
		require.NoError(t, err)

		var slot uint256.Int
		beaconSlot := types.Hash{}
		uint256.NewInt(header.Time % 8191).WriteToSlice(beaconSlot[:])
		ibs.GetState(beaconRootsAddr, &beaconSlot, &slot)
		require.Equal(t, header.Time, slot.Uint64())

		historySlot := types.Hash{}
		ibs.GetState(historyAddr, &historySlot, &slot)
		historyValue := slot.Bytes32()
		require.Equal(t, genesisBlock.Hash().Bytes(), historyValue[:])

		slot0 := types.Hash{}
		ibs.GetState(modexpReaderAddr, &slot0, &slot)
		require.Equal(t, uint64(1), slot.Uint64())

		slot1 := types.Hash{}
		slot1[31] = 1
		ibs.GetState(modexpReaderAddr, &slot1, &slot)
		require.Equal(t, uint64(0x28), slot.Uint64())

		slot2 := types.Hash{}
		slot2[31] = 2
		ibs.GetState(modexpReaderAddr, &slot2, &slot)
		require.Equal(t, uint64(0xc8), slot.Uint64())

		slot3 := types.Hash{}
		slot3[31] = 3
		ibs.GetState(modexpReaderAddr, &slot3, &slot)
		slot3Value := slot.Bytes32()
		require.Equal(t, types.HexToHash("0x7f8a6c56dfa7eb36c5813257a65db0d4503a9c3b69afc9247d97efb4af16ac18").Bytes(), slot3Value[:])

		require.Equal(t, uint64(1), ibs.GetNonce(sender))
		require.Equal(t, "0x3635c9adc5de941454", ibs.GetBalance(sender).Hex())

		expectedRoot = ibs.IntermediateRoot()
		expectedReceiptsRoot = ethel.EthReceiptHash(block.Receipts{receipt})
		expectedBloom = block.CreateBloom(block.Receipts{receipt})
		require.Equal(t, types.HexToHash("0x15144d30c9924a0b9b5c95db79a51ce2cba0977842e212318c5a0fc3c47779a0"), expectedReceiptsRoot)

		writer := state.NewPlainStateWriter(txdb, txdb, 1)
		require.NoError(t, ibs.CommitBlock(cfg.RulesWithTimestamp(1, header.Time), writer))
		require.NoError(t, writer.WriteChangeSets())
		require.NoError(t, writer.WriteHistory())

		persisted := state.New(state.NewPlainStateReader(txdb))
		var persistedValue uint256.Int
		persisted.GetState(beaconRootsAddr, &beaconSlot, &persistedValue)
		require.Equal(t, header.Time, persistedValue.Uint64())

		persisted.GetState(historyAddr, &historySlot, &persistedValue)
		persistedHistoryValue := persistedValue.Bytes32()
		require.Equal(t, genesisBlock.Hash().Bytes(), persistedHistoryValue[:])

		persisted.GetState(modexpReaderAddr, &slot0, &persistedValue)
		require.Equal(t, uint64(1), persistedValue.Uint64())
		persisted.GetState(modexpReaderAddr, &slot1, &persistedValue)
		require.Equal(t, uint64(0x28), persistedValue.Uint64())
		persisted.GetState(modexpReaderAddr, &slot2, &persistedValue)
		require.Equal(t, uint64(0xc8), persistedValue.Uint64())
		persisted.GetState(modexpReaderAddr, &slot3, &persistedValue)
		persistedSlot3Value := persistedValue.Bytes32()
		require.Equal(t, types.HexToHash("0x7f8a6c56dfa7eb36c5813257a65db0d4503a9c3b69afc9247d97efb4af16ac18").Bytes(), persistedSlot3Value[:])
		require.Equal(t, uint64(1), persisted.GetNonce(sender))
		require.Equal(t, "0x3635c9adc5de941454", persisted.GetBalance(sender).Hex())

		verifyErr := error(nil)
		verifiedRoot, verifyErr = ethel.VerifyStateRoot(txdb)
		require.NoError(t, verifyErr)
		fullVerifiedRoot, verifyErr = ethel.FullStateRootVerify(txdb)
		require.NoError(t, verifyErr)
		return nil
	})
	require.NoError(t, err)
	t.Logf("in-memory root=%s verified root=%s full root=%s receiptsRoot=%s bloom=%x", expectedRoot, verifiedRoot, fullVerifiedRoot, expectedReceiptsRoot, expectedBloom.Bytes())
}

func hiveFirstEmptyPayload() *ExecutionPayloadV1 {
	return &ExecutionPayloadV1{
		ParentHash:    types.HexToHash("0xd462b6793c2895fd61cd63e098874774d3e03556e14ec40fb9861956e39eb8b0"),
		FeeRecipient:  types.Address{},
		StateRoot:     types.HexToHash("0xdac58864b1d70d65174a6ead9c462edc165ca9f05e32778b64eb7059a789ec74"),
		ReceiptsRoot:  types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.HexToHash("0x1a6da4e66eed7d5e89e8fb8ce0cff225c8adebc0af569fd8533a1107cc8d7f8b"),
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(0x2ffbd2),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(0x1235),
		ExtraData:     types.Hex2Bytes("d883011007846765746888676f312e32352e38856c696e7578"),
		BaseFeePerGas: hexutil.Uint64(0x342770c0),
		BlockHash:     types.HexToHash("0xcac5f6605258df666b76d2d38e9549be9aae0a44dc08493888d21b23f3687ef3"),
		Transactions:  []hexutil.Bytes{},
	}
}

func newHiveEngineGenesisDB(t *testing.T) (kv.RwDB, *conf.Genesis, *block.Block) {
	t.Helper()

	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	genesis := loadHiveEngineGenesisFixture(t)
	addHiveEngineTestAccounts(genesis)

	db := memdb.NewTestDB(t)
	var genesisBlock *block.Block
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)
	return db, genesis, genesisBlock
}

func newHiveEngineGenesisMDBXDB(t *testing.T) (kv.RwDB, *conf.Genesis, *block.Block) {
	t.Helper()

	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	genesis := loadHiveEngineGenesisFixture(t)
	addHiveEngineTestAccounts(genesis)

	path := filepath.Join(t.TempDir(), "chaindata")
	openDB := func() kv.RwDB {
		db, err := mdbx.NewMDBX(logv3.New()).
			Path(path).
			Label(kv.ChainDB).
			Open(context.Background())
		require.NoError(t, err)
		return db
	}

	db := openDB()
	var genesisBlock *block.Block
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)
	db.Close()

	reopened := openDB()
	return reopened, genesis, genesisBlock
}
