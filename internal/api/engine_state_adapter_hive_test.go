package api

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ethel"
	vmcore "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	logv3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestRewindEngineValidationStateUsesHistoricalParent(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(t)
	var addr types.Address
	addr[19] = 0x42
	emptyCodeHash := crypto.Keccak256Hash(nil)
	accountAt := func(nonce uint64) *account.StateAccount {
		return &account.StateAccount{
			Initialised: true,
			Nonce:       nonce,
			CodeHash:    emptyCodeHash,
		}
	}

	var canonicalHashes [3]types.Hash
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		original := accountAt(0)
		if err := tx.Put(modules.Account, addr[:], original.MarshalV2()); err != nil {
			return err
		}
		previous := original
		var headHash types.Hash
		for number := uint64(1); number <= 2; number++ {
			next := accountAt(number)
			writer := state.NewPlainStateWriter(tx, tx, number)
			if err := writer.UpdateAccountData(addr, previous, next); err != nil {
				return err
			}
			if err := writer.WriteChangeSets(); err != nil {
				return err
			}
			if err := writer.WriteHistory(); err != nil {
				return err
			}
			header := &block.Header{Number: uint256.NewInt(number)}
			rawdb.WriteHeader(tx, header)
			headHash = header.Hash()
			canonicalHashes[number] = headHash
			if err := rawdb.WriteCanonicalHash(tx, headHash, number); err != nil {
				return err
			}
			previous = next
		}
		rawdb.WriteHeadBlockHash(tx, headHash)
		return ethel.RebuildHashedState(tx)
	}))

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	rewoundState, err := rewindEngineValidationState(tx, 2)
	require.NoError(t, err)
	require.True(t, rewoundState)
	var rewound account.StateAccount
	rewoundData, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.NoError(t, rewound.DecodeForStorageV2(rewoundData))
	require.Equal(t, uint64(1), rewound.Nonce)
	tx.Rollback()

	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		var persisted account.StateAccount
		data, err := tx.GetOne(modules.Account, addr[:])
		if err != nil {
			return err
		}
		if err := persisted.DecodeForStorageV2(data); err != nil {
			return err
		}
		require.Equal(t, uint64(2), persisted.Nonce)
		return nil
	}))

	adapter := NewEngineStateAdapter(db, nil, nil, nil)
	require.NoError(t, adapter.reorgCanonicalTo(canonicalHashes[1]))
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		var persisted account.StateAccount
		data, err := tx.GetOne(modules.Account, addr[:])
		if err != nil {
			return err
		}
		if err := persisted.DecodeForStorageV2(data); err != nil {
			return err
		}
		require.Equal(t, uint64(1), persisted.Nonce)
		require.Equal(t, canonicalHashes[1], rawdb.ReadHeadBlockHash(tx))
		blockTwoHash, err := rawdb.ReadCanonicalHash(tx, 2)
		require.NoError(t, err)
		require.Equal(t, types.Hash{}, blockTwoHash)
		return nil
	}))
}

func TestRewindEngineValidationStateUsesExecutionProgressWhenHeadLags(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = ethel.ChainTableCfg(modules.N42TableCfg)
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(t)
	var addr types.Address
	addr[19] = 0x43
	emptyCodeHash := crypto.Keccak256Hash(nil)
	accountAt := func(nonce uint64) *account.StateAccount {
		return &account.StateAccount{
			Initialised: true,
			Nonce:       nonce,
			CodeHash:    emptyCodeHash,
		}
	}

	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		previous := accountAt(0)
		if err := tx.Put(modules.Account, addr[:], previous.MarshalV2()); err != nil {
			return err
		}
		var staleHead types.Hash
		for number := uint64(1); number <= 2; number++ {
			next := accountAt(number)
			writer := state.NewPlainStateWriter(tx, tx, number)
			if err := writer.UpdateAccountData(addr, previous, next); err != nil {
				return err
			}
			if err := writer.WriteChangeSets(); err != nil {
				return err
			}
			if err := writer.WriteHistory(); err != nil {
				return err
			}
			header := &block.Header{Number: uint256.NewInt(number)}
			rawdb.WriteHeader(tx, header)
			if err := rawdb.WriteCanonicalHash(tx, header.Hash(), number); err != nil {
				return err
			}
			if number == 1 {
				staleHead = header.Hash()
			}
			previous = next
		}
		rawdb.WriteHeadBlockHash(tx, staleHead)
		return ethel.WriteProgress(tx, 2)
	}))

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	rewound, err := rewindEngineValidationState(tx, 2)
	require.NoError(t, err)
	require.True(t, rewound)
	var accountAtParent account.StateAccount
	data, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.NoError(t, accountAtParent.DecodeForStorageV2(data))
	require.Equal(t, uint64(1), accountAtParent.Nonce)
	tx.Rollback()
}

func TestWithParentStateRewindsToOverlayBase(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(t)
	var sideAddr, canonicalOnlyAddr types.Address
	sideAddr[19] = 0x44
	canonicalOnlyAddr[19] = 0x45
	emptyCodeHash := crypto.Keccak256Hash(nil)
	accountAt := func(nonce uint64) *account.StateAccount {
		return &account.StateAccount{
			Initialised: true,
			Nonce:       nonce,
			CodeHash:    emptyCodeHash,
		}
	}

	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		genesisAccount := accountAt(0)
		if err := tx.Put(modules.Account, sideAddr[:], genesisAccount.MarshalV2()); err != nil {
			return err
		}

		blockOneAccount := accountAt(1)
		canonicalOnlyAccount := accountAt(1)
		writer := state.NewPlainStateWriter(tx, tx, 1)
		if err := writer.UpdateAccountData(sideAddr, genesisAccount, blockOneAccount); err != nil {
			return err
		}
		if err := writer.UpdateAccountData(canonicalOnlyAddr, &account.StateAccount{}, canonicalOnlyAccount); err != nil {
			return err
		}
		if err := writer.WriteChangeSets(); err != nil {
			return err
		}

		blockTwoAccount := accountAt(2)
		writer = state.NewPlainStateWriter(tx, tx, 2)
		if err := writer.UpdateAccountData(sideAddr, blockOneAccount, blockTwoAccount); err != nil {
			return err
		}
		if err := writer.WriteChangeSets(); err != nil {
			return err
		}

		var headHash types.Hash
		for number := uint64(1); number <= 2; number++ {
			header := &block.Header{Number: uint256.NewInt(number)}
			rawdb.WriteHeader(tx, header)
			headHash = header.Hash()
			if err := rawdb.WriteCanonicalHash(tx, headHash, number); err != nil {
				return err
			}
		}
		rawdb.WriteHeadBlockHash(tx, headHash)
		return ethel.RebuildHashedState(tx)
	}))

	sideAccount := accountAt(7)
	overlay := &engineStateOverlay{
		accounts:           map[types.Address][]byte{sideAddr: sideAccount.MarshalV2()},
		baseBlockNumber:    0,
		hasBaseBlockNumber: true,
	}
	require.NoError(t, withParentState(db, 2, overlay, func(_ kv.Tx, reader state.StateReader, _ *state.IntraBlockState) error {
		gotSideAccount, err := reader.ReadAccountData(sideAddr)
		require.NoError(t, err)
		require.NotNil(t, gotSideAccount)
		require.Equal(t, uint64(7), gotSideAccount.Nonce)

		gotCanonicalOnlyAccount, err := reader.ReadAccountData(canonicalOnlyAddr)
		require.NoError(t, err)
		require.Nil(t, gotCanonicalOnlyAccount)
		return nil
	}))
}

func TestResolveCanonicalStoredHashRejectsNonCanonicalHeader(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(t)
	canonicalHeader := &block.Header{Number: uint256.NewInt(1), Extra: []byte("canonical")}
	sideHeader := &block.Header{Number: uint256.NewInt(1), Extra: []byte("side")}
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, canonicalHeader)
		rawdb.WriteHeader(tx, sideHeader)
		return rawdb.WriteCanonicalHash(tx, canonicalHeader.Hash(), 1)
	}))

	adapter := NewEngineStateAdapter(db, nil, nil, nil)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		resolvedCanonical, err := adapter.resolveCanonicalStoredHash(tx, canonicalHeader.Hash())
		require.NoError(t, err)
		require.Equal(t, canonicalHeader.Hash(), resolvedCanonical)

		resolvedSide, err := adapter.resolveCanonicalStoredHash(tx, sideHeader.Hash())
		require.NoError(t, err)
		require.Equal(t, types.Hash{}, resolvedSide)
		return nil
	}))
}

func TestHiveEngineGenesisFixtureMatchesCurrentGethRoot(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	genesis := loadHiveEngineGenesisFixture(t)

	const (
		expectedStateRoot = "0x84308e7d0abf860412f4a0c6fc25709a6e9eaba20a0d085a0344d271f40109ec"
		expectedHash      = "0x67ead97eb79b47a1638659942384143f36ed44275d4182799875ab5a87324055"
	)

	db := memdb.NewTestDB(t)
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).WriteGenesisState(tx)
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

func TestEthELBuildsAndValidatesFirstCancunPayloadWithoutBlockchain(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)
	genesis := &internalcore.GenesisBlock{GenesisConfig: &conf.Genesis{
		Config:     cfg,
		GasLimit:   30_000_000,
		Difficulty: uint256.NewInt(0),
		Timestamp:  1,
		BaseFee:    uint256.NewInt(7),
	}}
	var genesisBlock *block.Block
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		genesisBlock = blk
		return err
	}))
	require.NotNil(t, genesisBlock)

	backend := &API{
		db:            db,
		engine:        &apiTestEngine{},
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIBlob(NewBlockChainAPI(backend))
	engine.SetStateAdapter(NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{}))
	beaconRoot := types.Hash{0x44}
	withdrawals := []*Withdrawal{{
		Index:          1,
		ValidatorIndex: 1,
		Address:        types.HexToAddress("0x2222222222222222222222222222222222222222"),
		Amount:         100,
	}}
	resp, err := engine.ForkchoiceUpdatedV3(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: ethCompatibleBlockHash(genesisBlock, cfg),
	}, &PayloadAttributesV3{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x33},
		SuggestedFeeRecipient: types.Address{},
		Withdrawals:           withdrawals,
		ParentBeaconBlockRoot: &beaconRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	built, err := engine.GetPayloadV3(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.NotEqual(t, genesisBlock.StateRoot(), built.ExecutionPayload.StateRoot)
	status, err := engine.NewPayloadV3(context.Background(), built.ExecutionPayload, []types.Hash{}, &beaconRoot)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, status.Status)
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

func TestHiveEngineStateAdapterRejectsInvalidWireGasLimit(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)
	payload := hiveFirstEmptyPayload()
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	header := blk.Header().(*block.Header)
	header.GasLimit *= 2
	blk.WithSeal(header)

	valid, _, err := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).
		ExecutePayloadFromWire(blk.(*block.Block), nil)
	require.NoError(t, err)
	require.False(t, valid)
}

func TestHiveEngineStateAdapterBatchHeaderLookupSeesUncommittedPayload(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)
	payload := hiveFirstEmptyPayload()
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	adapter := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{})
	adapter.SetBatchTx(tx)
	defer adapter.SetBatchTx(nil)

	valid, _, err := adapter.ExecutePayload(blk.(*block.Block))
	require.NoError(t, err)
	require.True(t, valid)
	parent := adapter.HeaderByHash(payload.BlockHash)
	require.NotNil(t, parent)
	require.Equal(t, uint64(payload.GasLimit), parent.GasLimit)
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

func TestHiveEngineStateAdapterForkchoiceUpdatedTracksMDBXHead(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisMDBXDB(t)
	defer db.Close()

	payload := hiveFirstEmptyPayload()
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(blk, genesis.Config))

	adapter := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).WithHeadMarker(true)
	valid, stateRoot, err := adapter.ExecutePayload(blk.(*block.Block))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, payload.StateRoot, stateRoot)

	err = adapter.ForkchoiceUpdated(payload.BlockHash, payload.BlockHash, payload.BlockHash)
	require.NoError(t, err)

	head := adapter.CurrentHead()
	require.NotNil(t, head)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(head, genesis.Config))
	require.Equal(t, payload.BlockHash, adapter.CurrentHeadHash())
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		marker, ok := ethel.ReadHeadMarker(tx)
		require.True(t, ok)
		require.Equal(t, uint64(payload.BlockNumber), marker)
		storedHash := rawdb.ReadHeadBlockHash(tx)
		require.Equal(t, storedHash, rawdb.ReadForkchoiceSafeHash(tx))
		require.Equal(t, storedHash, rawdb.ReadForkchoiceFinalizedHash(tx))
		return nil
	}))
}

func TestHiveEngineStateAdapterForkchoiceUpdatedExposesLatestBlock(t *testing.T) {
	db, genesis, genesisBlock := newHiveEngineGenesisMDBXDB(t)
	defer db.Close()

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: genesis.Config,
		db:     db,
	}
	backend := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   genesis.Config,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(backend))
	adapter := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{})
	engine.SetStateAdapter(adapter)

	payload := hiveFirstEmptyPayload()
	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, backend.engineOverlay.receiptsByBlockHash(payload.BlockHash))
	require.Equal(t, payload.ParentHash, adapter.CurrentHeadHash())

	forkchoiceResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.ParentHash,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, forkchoiceResp.PayloadStatus.Status)
	require.Equal(t, payload.BlockHash, adapter.CurrentHeadHash())

	latest, err := NewBlockChainAPI(backend).GetBlockByNumber(context.Background(), jsonrpc.LatestBlockNumber, false)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, avmtypes.FromastHash(payload.BlockHash), latest["hash"])
	require.Equal(t, uint64(payload.BlockNumber), latest["number"].(*hexutil.Big).ToInt().Uint64())
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
	fixture := loadStablePragueModexpFixture(t)

	beaconRootsAddr := params.BeaconRootsAddress
	historyAddr := vmcore.HistoryStorageAddress
	modexpReaderAddr := types.HexToAddress("0x2af898aa328a87cfb23fbaf9302887d7c37df8fe")
	sender := types.HexToAddress("0xe88c5d94c986f78708f74a3ed89b9ea177cbb8e5")
	feeRecipient := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	uint64Ptr := func(v uint64) *uint64 { return &v }

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      fixture.Pre,
			Number:     0,
			GasLimit:   0x07270e00,
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   types.Address{},
			ExtraData:  fixture.GenesisExtraData,
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
	require.Equal(t, fixture.GenesisBlockHash, ethCompatibleBlockHash(genesisBlock, cfg))

	rawTx := hexutil.MustDecode("0x02f9011801808007830238a0942af898aa328a87cfb23fbaf9302887d7c37df8fe80b8b4000000000000000000000000000000000000000000000000000000000000002800000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000028e8e77626586f73b955364c7b4bbf0bb7f7685ebd40e852b164633a4acbd3244c000102030405060701fffffff01681d2220bfea4bb888a5543db8c0916274ddb1ea93b144c042c01d8164c950001020304050607c080a0efac5255b8dc509c4604cf399f583589203c5f2b2ed59d0cbe7fd14e1866a8eda07c0b0f0c7f988e91c064f0290249de70ca5e0f0ce0833987844ed38abd46a55c")
	tx, err := transaction.DecodeEthereumTransaction(rawTx)
	require.NoError(t, err)
	parentHash := ethCompatibleBlockHash(genesisBlock, cfg)

	header := &block.Header{
		ParentHash:       parentHash,
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
		require.Equal(t, parentHash.Bytes(), historyValue[:])

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
		require.Equal(t, parentHash.Bytes(), persistedHistoryValue[:])

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
		for addr, expectedAccount := range fixture.PostState {
			require.Equalf(t, expectedAccount.Nonce, persisted.GetNonce(addr), "nonce mismatch for %s", addr.Hex())

			expectedBalance := new(big.Int)
			if expectedAccount.Balance != "" {
				ok := false
				expectedBalance, ok = expectedBalance.SetString(strings.TrimPrefix(expectedAccount.Balance, "0x"), 16)
				require.Truef(t, ok, "invalid expected balance for %s", addr.Hex())
			}
			require.Zerof(t, persisted.GetBalance(addr).ToBig().Cmp(expectedBalance), "balance mismatch for %s", addr.Hex())

			expectedCode := append([]byte(nil), expectedAccount.Code...)
			actualCode := append([]byte(nil), persisted.GetCode(addr)...)
			require.Equalf(t, expectedCode, actualCode, "code mismatch for %s", addr.Hex())

			for slotHash, expectedValue := range expectedAccount.Storage {
				var actualValue uint256.Int
				persisted.GetState(addr, &slotHash, &actualValue)
				actualBytes := actualValue.Bytes32()
				require.Equalf(t, expectedValue.Bytes(), actualBytes[:], "storage mismatch for %s slot %s", addr.Hex(), slotHash.Hex())
			}
		}

		verifyErr := error(nil)
		verifiedRoot, verifyErr = ethel.VerifyStateRoot(txdb)
		require.NoError(t, verifyErr)
		// The fixture state is still uncommitted in txdb, so verification must use
		// the sequential path over this transaction rather than worker RoTxs.
		fullVerifiedRoot, verifyErr = ethel.FullStateRootVerify(nil, txdb, 1)
		require.NoError(t, verifyErr)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, types.HexToHash("0xe64924afb5c8caf763266ec9f9a12a6510c52ea3e3f4281ee9db9b51277b0b1d"), verifiedRoot)
	require.Equal(t, verifiedRoot, fullVerifiedRoot)

	payloadHeader := block.CopyHeader(header)
	payloadHeader.Root = verifiedRoot
	payloadHeader.GasUsed = 0x1b3f4
	payloadHeader.ReceiptHash = expectedReceiptsRoot
	payloadHeader.Bloom = expectedBloom
	payloadHeaderHash := ethCompatibleHeaderHash(payloadHeader, cfg)
	payloadTx, err := transaction.DecodeEthereumTransaction(rawTx)
	require.NoError(t, err)
	execDB := memdb.NewTestDB(t)
	var execGenesisBlock *block.Block
	err = execDB.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		execGenesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, execGenesisBlock)
	require.Equal(t, fixture.GenesisBlockHash, ethCompatibleBlockHash(execGenesisBlock, cfg))

	execPayload := block.NewBlock(payloadHeader, []*transaction.Transaction{payloadTx}).(*block.Block)
	require.Equal(t, payloadHeaderHash, ethCompatibleBlockHash(execPayload, cfg))
	valid, stateRoot, err := NewEngineStateAdapter(execDB, nil, cfg, &apiTestEngine{}).ExecutePayload(execPayload)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, verifiedRoot, stateRoot)
	require.NoError(t, execDB.View(context.Background(), func(dbtx kv.Tx) error {
		storedTx, storedBlockHash, blockNumber, txIndex, err := rawdb.ReadTransactionByHash(dbtx, payloadTx.Hash())
		require.NoError(t, err)
		require.NotNil(t, storedTx)
		require.Equal(t, payloadTx.Hash(), storedTx.Hash())
		require.Equal(t, uint64(1), blockNumber)
		require.Equal(t, uint64(0), txIndex)

		storedBlock, err := rawdb.ReadBlockByHash(dbtx, storedBlockHash)
		require.NoError(t, err)
		require.NotNil(t, storedBlock)
		storedReceipts := rawdb.ReadReceipts(dbtx, storedBlock, nil)
		require.Len(t, storedReceipts, 1)
		require.Equal(t, payloadTx.Hash(), storedReceipts[0].TxHash)
		require.Equal(t, storedBlockHash, storedReceipts[0].BlockHash)
		return nil
	}))
	t.Logf("in-memory root=%s verified root=%s full root=%s receiptsRoot=%s bloom=%x", expectedRoot, verifiedRoot, fullVerifiedRoot, expectedReceiptsRoot, expectedBloom.Bytes())
}

func TestEngineStateAdapterAcceptsStableShanghaiWithdrawalsFixture(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	stateFixture := loadStableShanghaiWithdrawalFixture(t)
	payload := loadStableShanghaiWithdrawalEnginePayloadFixture(t)

	for _, tc := range []struct {
		name    string
		genesis *conf.Genesis
	}{
		{
			name: "manual_block_forks",
			genesis: &conf.Genesis{
				Config: &params.ChainConfig{
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
				},
				Alloc:      stateFixture.Pre,
				Number:     0,
				GasLimit:   uint64(payload.GasLimit),
				Difficulty: uint256.NewInt(0),
				Timestamp:  0,
				BaseFee:    uint256.NewInt(uint64(payload.BaseFeePerGas)),
				Coinbase:   types.Address{},
				ExtraData:  stateFixture.GenesisExtraData,
			},
		},
		{
			name: "hive_timestamp_forks",
			genesis: func() *conf.Genesis {
				genesis := &conf.Genesis{
					Alloc:      stateFixture.Pre,
					Number:     0,
					GasLimit:   uint64(payload.GasLimit),
					Difficulty: uint256.NewInt(0),
					Timestamp:  0,
					BaseFee:    uint256.NewInt(uint64(payload.BaseFeePerGas)),
					Coinbase:   types.Address{},
					ExtraData:  stateFixture.GenesisExtraData,
				}
				applied := conf.ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
					value, ok := stableShanghaiWithdrawalHiveEnv()[key]
					return value, ok
				})
				require.True(t, applied)
				return genesis
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.genesis.Config
			require.NotNil(t, cfg)

			for _, dbtc := range []struct {
				name   string
				openDB func(*testing.T) kv.RwDB
			}{
				{
					name: "memdb",
					openDB: func(t *testing.T) kv.RwDB {
						return memdb.NewTestDB(t)
					},
				},
				{
					name: "mdbx_reopen",
					openDB: func(t *testing.T) kv.RwDB {
						path := filepath.Join(t.TempDir(), "chaindata")
						open := func() kv.RwDB {
							db, err := mdbx.NewMDBX(logv3.New()).
								Path(path).
								Label(kv.ChainDB).
								Open(context.Background())
							require.NoError(t, err)
							return db
						}
						db := open()
						db.Close()
						return open()
					},
				},
			} {
				t.Run(dbtc.name, func(t *testing.T) {
					genesis := &internalcore.GenesisBlock{GenesisConfig: tc.genesis}
					db := dbtc.openDB(t)
					if closer, ok := db.(interface{ Close() }); ok {
						t.Cleanup(func() { closer.Close() })
					}

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
					require.Equal(t, stateFixture.GenesisBlockHash, ethCompatibleBlockHash(genesisBlock, cfg))
					require.Equal(t, ethCompatibleBlockHash(genesisBlock, cfg), payload.ParentHash)

					overlayBlockIface, err := executionPayloadV2ToBlock(payload)
					require.NoError(t, err)
					overlayBlock, ok := overlayBlockIface.(*block.Block)
					require.True(t, ok)
					require.Equal(t, payload.BlockHash, ethCompatibleEngineBlockHash(overlayBlock, cfg, enginePayloadHashOptions{
						includeWithdrawals: true,
						withdrawals:        payload.Withdrawals,
					}))

					chain := &canonicalCheckChainStub{
						header: genesisBlock.Header().(*block.Header),
						blk:    genesisBlock,
						config: cfg,
						db:     db,
					}
					api := &API{
						bc:            chain,
						db:            db,
						engine:        &apiTestEngine{},
						chainConfig:   cfg,
						engineOverlay: newEngineOverlay(),
					}
					engine := NewEngineAPIV1(NewBlockChainAPI(api))

					overlayState, receipts, err := engine.executePayloadOnParentStateAndValidateWithWithdrawals(overlayBlock, payload.ParentHash, nil, nil, payload.Withdrawals)
					require.NoError(t, err)
					require.NotNil(t, overlayState)
					require.Empty(t, receipts)
					require.Equal(t, payload.StateRoot, overlayBlock.StateRoot())

					stateAdapterBlockIface, err := executionPayloadV2ToBlock(payload)
					require.NoError(t, err)
					stateAdapterBlock, ok := stateAdapterBlockIface.(*block.Block)
					require.True(t, ok)

					adapter := NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{})
					engine.SetStateAdapter(adapter)
					resp, err := engine.NewPayloadV2(context.Background(), payload)
					require.NoError(t, err)
					require.Equal(t, PayloadStatusValid, resp.Status)
					require.NotNil(t, resp.LatestValidHash)
					require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
					require.Equal(t, payload.ParentHash, adapter.CurrentHeadHash())

					forkchoiceResp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
						HeadBlockHash:      payload.BlockHash,
						SafeBlockHash:      payload.BlockHash,
						FinalizedBlockHash: payload.ParentHash,
					}, nil)
					require.NoError(t, err)
					require.Equal(t, PayloadStatusValid, forkchoiceResp.PayloadStatus.Status)
					require.Equal(t, payload.BlockHash, adapter.CurrentHeadHash())

					err = db.View(context.Background(), func(tx kv.Tx) error {
						persisted := state.New(state.NewPlainStateReader(tx))
						for addr, expectedAccount := range stateFixture.PostState {
							expectedBalance := new(big.Int)
							if expectedAccount.Balance != "" {
								ok := false
								expectedBalance, ok = expectedBalance.SetString(strings.TrimPrefix(expectedAccount.Balance, "0x"), 16)
								require.Truef(t, ok, "invalid expected balance for %s", addr.Hex())
							}
							require.Zerof(t, persisted.GetBalance(addr).ToBig().Cmp(expectedBalance), "balance mismatch for %s", addr.Hex())
						}
						return nil
					})
					require.NoError(t, err)
					require.Equal(t, payload.StateRoot, stateAdapterBlock.StateRoot())
				})
			}
		})
	}
}

func TestEngineAPIv4AcceptsStablePragueModexpCallcodeFixture(t *testing.T) {
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
	stateFixture := loadStablePragueModexpFixture(t)
	payloadFixture := loadStablePragueModexpEnginePayloadFixture(t)

	for _, tc := range []struct {
		name   string
		openDB func(*testing.T) kv.RwDB
	}{
		{
			name: "memdb",
			openDB: func(t *testing.T) kv.RwDB {
				return memdb.NewTestDB(t)
			},
		},
		{
			name: "mdbx_reopen",
			openDB: func(t *testing.T) kv.RwDB {
				path := filepath.Join(t.TempDir(), "chaindata")
				open := func() kv.RwDB {
					db, err := mdbx.NewMDBX(logv3.New()).
						Path(path).
						Label(kv.ChainDB).
						Open(context.Background())
					require.NoError(t, err)
					return db
				}
				db := open()
				db.Close()
				return open()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := tc.openDB(t)
			if closer, ok := db.(interface{ Close() }); ok {
				t.Cleanup(func() { closer.Close() })
			}

			genesis := &internalcore.GenesisBlock{
				GenesisConfig: &conf.Genesis{
					Config:     cfg,
					Alloc:      stateFixture.Pre,
					Number:     0,
					GasLimit:   0x07270e00,
					Difficulty: uint256.NewInt(0),
					Timestamp:  0,
					BaseFee:    uint256.NewInt(7),
					Coinbase:   types.Address{},
					ExtraData:  stateFixture.GenesisExtraData,
				},
			}

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
			require.Equal(t, stateFixture.GenesisBlockHash, ethCompatibleBlockHash(genesisBlock, cfg))

			payload := *payloadFixture.Payload
			require.Equal(t, ethCompatibleBlockHash(genesisBlock, cfg), payload.ParentHash)

			blk, err := executionPayloadV4ToBlock(&payload)
			require.NoError(t, err)
			blk = applyExecutionPayloadForkFields(blk, nil, nil, nil, &payloadFixture.ParentBeaconBlockRoot, payloadFixture.ExecutionRequests)
			requestsHash := executionRequestsHash(payloadFixture.ExecutionRequests)
			require.Equal(t, payload.BlockHash, ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
				includeWithdrawals: true,
				withdrawals:        payload.Withdrawals,
				includeBlobFields:  true,
				parentBeaconRoot:   &payloadFixture.ParentBeaconBlockRoot,
				requestsHash:       &requestsHash,
			}))

			chain := &canonicalCheckChainStub{
				header: genesisBlock.Header().(*block.Header),
				blk:    genesisBlock,
				config: cfg,
				db:     db,
			}
			api := &API{
				bc:            chain,
				db:            db,
				engine:        &apiTestEngine{},
				chainConfig:   cfg,
				engineOverlay: newEngineOverlay(),
			}
			engine := NewEngineAPIv4(NewBlockChainAPI(api))
			engine.SetStateAdapter(NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{}))

			resp, err := engine.NewPayloadV4(
				context.Background(),
				&payload,
				payloadFixture.ExpectedBlobVersionedHashes,
				&payloadFixture.ParentBeaconBlockRoot,
				cloneHexutilBytesList(payloadFixture.ExecutionRequests),
			)
			require.NoError(t, err)
			require.Equal(t, PayloadStatusValid, resp.Status)
			require.NotNil(t, resp.LatestValidHash)
			require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
		})
	}
}

func TestEngineAPIv4ForkchoiceUpdatedAcceptsStablePragueModexpCallcodeFixture(t *testing.T) {
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
	stateFixture := loadStablePragueModexpFixture(t)
	payloadFixture := loadStablePragueModexpEnginePayloadFixture(t)

	db := memdb.NewTestDB(t)
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      stateFixture.Pre,
			Number:     0,
			GasLimit:   0x07270e00,
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   types.Address{},
			ExtraData:  stateFixture.GenesisExtraData,
		},
	}

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

	payload := *payloadFixture.Payload
	require.Equal(t, ethCompatibleBlockHash(genesisBlock, cfg), payload.ParentHash)

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	adapter := NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{})
	engine.SetStateAdapter(adapter)

	resp, err := engine.NewPayloadV4(
		context.Background(),
		&payload,
		payloadFixture.ExpectedBlobVersionedHashes,
		&payloadFixture.ParentBeaconBlockRoot,
		cloneHexutilBytesList(payloadFixture.ExecutionRequests),
	)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *resp.LatestValidHash)

	fcuResp, err := engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.BlockHash,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, fcuResp)
	require.Equal(t, PayloadStatusValid, fcuResp.PayloadStatus.Status)
	require.NotNil(t, fcuResp.PayloadStatus.LatestValidHash)
	require.Equal(t, payload.BlockHash, *fcuResp.PayloadStatus.LatestValidHash)
	require.Equal(t, payload.BlockHash, adapter.CurrentHeadHash())
}

func TestEngineAPIv4AcceptsStablePragueModexpCallcodeFixtureWithHiveEnvGenesis(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	stateFixture := loadStablePragueModexpFixture(t)
	payloadFixture := loadStablePragueModexpEnginePayloadFixture(t)

	genesis := &conf.Genesis{
		Alloc:      stateFixture.Pre,
		Number:     0,
		GasLimit:   0x07270e00,
		Difficulty: uint256.NewInt(0),
		Timestamp:  0,
		BaseFee:    uint256.NewInt(7),
		Coinbase:   types.Address{},
		ExtraData:  stateFixture.GenesisExtraData,
	}
	applied := conf.ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
		value, ok := stablePragueModexpHiveEnv()[key]
		return value, ok
	})
	require.True(t, applied)
	require.NotNil(t, genesis.Config)
	require.Equal(t, params.Faker, genesis.Config.Consensus)
	require.Equal(t, uint64(1), genesis.Config.ChainID.Uint64())
	require.Equal(t, uint64(0), genesis.Config.PragueTime.Uint64())
	require.Equal(t, uint64(0), genesis.Config.CancunTime.Uint64())
	require.Equal(t, uint64(0), genesis.Config.ShanghaiTime.Uint64())
	require.Equal(t, uint64(0), genesis.Config.MergeNetsplitBlock.Uint64())
	require.Equal(t, uint64(0), genesis.Config.TerminalTotalDifficulty.Uint64())

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
	require.Equal(t, stateFixture.GenesisBlockHash, ethCompatibleBlockHash(genesisBlock, genesis.Config))

	payload := *payloadFixture.Payload
	require.Equal(t, ethCompatibleBlockHash(genesisBlock, genesis.Config), payload.ParentHash)

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
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	engine.SetStateAdapter(NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}))

	resp, err := engine.NewPayloadV4(
		context.Background(),
		&payload,
		payloadFixture.ExpectedBlobVersionedHashes,
		&payloadFixture.ParentBeaconBlockRoot,
		cloneHexutilBytesList(payloadFixture.ExecutionRequests),
	)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
}

func TestEngineAPIv4RejectsInvalidPragueWithdrawalRequestsWithStateAdapter(t *testing.T) {
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
		PectraTime:            big.NewInt(0),
		OsakaTime:             big.NewInt(0),
	}
	db := memdb.NewTestDB(t)

	reqA := &vmcore.WithdrawalRequest{
		SourceAddress: types.HexToAddress("0x1000000000000000000000000000000000000001"),
		Amount:        0,
	}
	reqA.ValidatorPubkey[47] = 0x01
	reqB := &vmcore.WithdrawalRequest{
		SourceAddress: types.HexToAddress("0x1000000000000000000000000000000000000002"),
		Amount:        0,
	}
	reqB.ValidatorPubkey[47] = 0x02

	actualSerialized := append(reqA.Serialize(), reqB.Serialize()...)
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				vmcore.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    buildReturnDataContract(actualSerialized),
				},
				vmcore.ConsolidationRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    []byte{byte(vmcore.STOP)},
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
		},
	}

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

	parentHash := ethCompatibleBlockHash(genesisBlock, cfg)
	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	engine.SetStateAdapter(NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{}))

	beaconRoot := types.Hash{0x77}
	executionRequests := []hexutil.Bytes{
		append([]byte{WithdrawalRequestType}, append(reqB.Serialize(), reqA.Serialize()...)...),
	}
	payload := &ExecutionPayloadV4{
		ParentHash:    parentHash,
		FeeRecipient:  types.Address{},
		StateRoot:     types.Hash{0x01},
		ReceiptsRoot:  types.Hash{0x02},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x03},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(7),
		Transactions:  nil,
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}

	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)
	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, &beaconRoot, executionRequests)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid requests hash")
	syncExecutionPayloadV4OutputsFromBlock(t, payload, blk)
	requestsHash := executionRequestsHash(executionRequests)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &beaconRoot, executionRequests)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, parentHash, *resp.LatestValidHash)
}

func TestEngineAPIv4RejectsEmptyPragueBlockWhenWithdrawalSystemContractMissingWithStateAdapter(t *testing.T) {
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
		PectraTime:            big.NewInt(0),
		OsakaTime:             big.NewInt(0),
	}
	db := memdb.NewTestDB(t)

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				vmcore.ConsolidationRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    []byte{byte(vmcore.STOP)},
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
		},
	}

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

	parentHash := ethCompatibleBlockHash(genesisBlock, cfg)
	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	engine.SetStateAdapter(NewEngineStateAdapter(db, nil, cfg, &apiTestEngine{}))

	beaconRoot := types.Hash{0x88}
	executionRequests := []hexutil.Bytes{}
	payload := &ExecutionPayloadV4{
		ParentHash:    parentHash,
		FeeRecipient:  types.Address{},
		StateRoot:     types.Hash{0x01},
		ReceiptsRoot:  types.Hash{0x02},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x03},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       hexutil.Uint64(0),
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexutil.Uint64(7),
		Transactions:  nil,
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}

	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)
	syncExecutionPayloadV4OutputsFromBlock(t, payload, blk)
	requestsHash := executionRequestsHash(executionRequests)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &beaconRoot, executionRequests)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, parentHash, *resp.LatestValidHash)
	require.NotNil(t, resp.ValidationError)
	require.Equal(t, "System contract address "+vmcore.WithdrawalRequestsAddress.Hex()+" has empty code", *resp.ValidationError)
}

func TestStablePragueModexpEntryPointExactGasFixtureMatchesExecutionState(t *testing.T) {
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
	stateFixture := loadStablePragueModexpEntryPointExactGasFixture(t)
	payloadFixture := loadStablePragueModexpEntryPointExactGasEnginePayloadFixture(t)

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      stateFixture.Pre,
			Number:     0,
			GasLimit:   uint64(payloadFixture.Payload.GasLimit),
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
			BaseFee:    uint256.NewInt(uint64(payloadFixture.Payload.BaseFeePerGas)),
			Coinbase:   types.Address{},
			ExtraData:  stateFixture.GenesisExtraData,
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
	require.Equal(t, stateFixture.GenesisBlockHash, ethCompatibleBlockHash(genesisBlock, cfg))
	require.Equal(t, ethCompatibleBlockHash(genesisBlock, cfg), payloadFixture.Payload.ParentHash)

	require.Len(t, payloadFixture.Payload.Transactions, 1)
	rawTx := []byte(payloadFixture.Payload.Transactions[0])
	tx, err := transaction.DecodeEthereumTransaction(rawTx)
	require.NoError(t, err)
	require.NotNil(t, payloadFixture.Payload.BlobGasUsed)
	require.NotNil(t, payloadFixture.Payload.ExcessBlobGas)

	uint64Ptr := func(v uint64) *uint64 { return &v }
	header := &block.Header{
		ParentHash:       payloadFixture.Payload.ParentHash,
		Number:           uint256.NewInt(uint64(payloadFixture.Payload.BlockNumber)),
		GasLimit:         uint64(payloadFixture.Payload.GasLimit),
		Time:             uint64(payloadFixture.Payload.Timestamp),
		BaseFee:          uint256.NewInt(uint64(payloadFixture.Payload.BaseFeePerGas)),
		Difficulty:       uint256.NewInt(0),
		Coinbase:         payloadFixture.Payload.FeeRecipient,
		BlobGasUsed:      uint64Ptr(uint64(*payloadFixture.Payload.BlobGasUsed)),
		ExcessBlobGas:    uint64Ptr(uint64(*payloadFixture.Payload.ExcessBlobGas)),
		ParentBeaconRoot: ptrToHash(payloadFixture.ParentBeaconBlockRoot),
	}

	var (
		verifiedRoot     types.Hash
		expectedBloom    block.Bloom
		expectedGasUsed  uint64
		expectedReceipts types.Hash
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
		expectedGasUsed = usedGas
		expectedReceipts = ethel.EthReceiptHash(block.Receipts{receipt})
		expectedBloom = block.CreateBloom(block.Receipts{receipt})

		_, err = internalcore.ProcessExecutionBlockEnd(block.Receipts{receipt}, cfg, ibs, header, &apiTestEngine{})
		require.NoError(t, err)

		writer := state.NewPlainStateWriter(txdb, txdb, 1)
		require.NoError(t, ibs.CommitBlock(cfg.RulesWithTimestamp(1, header.Time), writer))
		require.NoError(t, writer.WriteChangeSets())
		require.NoError(t, writer.WriteHistory())

		persisted := state.New(state.NewPlainStateReader(txdb))
		for addr, expectedAccount := range stateFixture.PostState {
			require.Equalf(t, expectedAccount.Nonce, persisted.GetNonce(addr), "nonce mismatch for %s", addr.Hex())

			expectedBalance := new(big.Int)
			if expectedAccount.Balance != "" {
				ok := false
				expectedBalance, ok = expectedBalance.SetString(strings.TrimPrefix(expectedAccount.Balance, "0x"), 16)
				require.Truef(t, ok, "invalid expected balance for %s", addr.Hex())
			}
			require.Zerof(
				t,
				persisted.GetBalance(addr).ToBig().Cmp(expectedBalance),
				"balance mismatch for %s got=%s want=%s",
				addr.Hex(),
				persisted.GetBalance(addr).Hex(),
				hexutil.EncodeBig(expectedBalance),
			)

			expectedCode := append([]byte(nil), expectedAccount.Code...)
			actualCode := append([]byte(nil), persisted.GetCode(addr)...)
			require.Equalf(t, expectedCode, actualCode, "code mismatch for %s", addr.Hex())

			for slotHash, expectedValue := range expectedAccount.Storage {
				var actualValue uint256.Int
				persisted.GetState(addr, &slotHash, &actualValue)
				actualBytes := actualValue.Bytes32()
				require.Equalf(t, expectedValue.Bytes(), actualBytes[:], "storage mismatch for %s slot %s", addr.Hex(), slotHash.Hex())
			}
		}

		var verifyErr error
		verifiedRoot, verifyErr = ethel.VerifyStateRoot(txdb)
		require.NoError(t, verifyErr)
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, uint64(payloadFixture.Payload.GasUsed), expectedGasUsed)
	require.Equal(t, payloadFixture.Payload.StateRoot, verifiedRoot)
	require.Equal(t, payloadFixture.Payload.ReceiptsRoot, expectedReceipts)
	require.Equal(t, payloadFixture.Payload.LogsBloom, hexutil.Bytes(expectedBloom.Bytes()))
}

type stablePragueModexpFixture struct {
	Pre              conf.GenesisAlloc `json:"pre"`
	PostState        conf.GenesisAlloc `json:"postState"`
	GenesisBlockHash types.Hash
	GenesisExtraData []byte
}

type stablePragueModexpEnginePayloadFixture struct {
	Payload                     *ExecutionPayloadV4
	ExpectedBlobVersionedHashes []types.Hash
	ParentBeaconBlockRoot       types.Hash
	ExecutionRequests           []hexutil.Bytes
}

type stablePragueModexpFixtureJSON struct {
	Pre                map[string]stablePragueModexpAccountJSON `json:"pre"`
	PostState          map[string]stablePragueModexpAccountJSON `json:"postState"`
	GenesisBlockHeader stablePragueModexpGenesisHeaderJSON      `json:"genesisBlockHeader"`
	EngineNewPayloads  []stablePragueModexpEngineCallJSON       `json:"engineNewPayloads"`
}

type stablePragueModexpGenesisHeaderJSON struct {
	Hash      string `json:"hash"`
	ExtraData string `json:"extraData"`
}

type stablePragueModexpEngineCallJSON struct {
	Params                   []json.RawMessage `json:"params"`
	NewPayloadVersion        string            `json:"newPayloadVersion"`
	ForkchoiceUpdatedVersion string            `json:"forkchoiceUpdatedVersion"`
}

type stablePragueModexpAccountJSON struct {
	Balance string            `json:"balance"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage"`
	Nonce   string            `json:"nonce"`
}

const stablePragueModexpFixtureKey = "tests/osaka/eip7883_modexp_gas_increase/test_modexp_thresholds.py::test_modexp_call_operations[fork_Prague-blockchain_test_engine_from_state_test-base-heavy-call_opcode_CALLCODE]"
const stablePragueModexpFixtureFile = "test_modexp_call_operations.json"
const stablePragueModexpEntryPointExactGasFixtureKey = "tests/osaka/eip7883_modexp_gas_increase/test_modexp_thresholds.py::test_modexp_used_in_transaction_entry_points[fork_Prague-blockchain_test_engine_from_state_test-exact_gas]"
const stablePragueModexpEntryPointFixtureFile = "test_modexp_used_in_transaction_entry_points.json"
const stableShanghaiWithdrawalFixtureKey = "tests/shanghai/eip4895_withdrawals/test_withdrawals.py::test_zero_amount[fork_Shanghai-blockchain_test_engine-four_withdrawals_one_with_value_one_with_max]"
const stableShanghaiWithdrawalFixtureFile = "test_zero_amount.json"

func stablePragueModexpFixturePathForFile(t *testing.T, fileName string) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	var candidates []string
	if cacheDir, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(
			cacheDir,
			"ethereum-execution-spec-tests",
			"cached_downloads",
			"ethereum",
			"execution-spec-tests",
			"v5.4.0",
			"fixtures_stable",
			"fixtures",
			"blockchain_tests_engine",
			"osaka",
			"eip7883_modexp_gas_increase",
			fileName,
		))
	}
	candidates = append(candidates, filepath.Join(
		filepath.Dir(currentFile),
		"..", "..", "..",
		"erigon2.7",
		"tests",
		"execution-spec-tests",
		"blockchain_tests_engine",
		"osaka",
		"eip7883_modexp_gas_increase",
		fileName,
	))

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skipf("stable EEST fixture missing; checked: %s", strings.Join(candidates, ", "))
	return ""
}

func stablePragueModexpFixturePath(t *testing.T) string {
	t.Helper()
	return stablePragueModexpFixturePathForFile(t, stablePragueModexpFixtureFile)
}

func stableShanghaiWithdrawalFixturePath(t *testing.T) string {
	t.Helper()
	return stableShanghaiWithdrawalFixturePathForFile(t, stableShanghaiWithdrawalFixtureFile)
}

func loadStablePragueModexpFixtureJSONForFileAndKey(t *testing.T, fileName, fixtureKey string) stablePragueModexpFixtureJSON {
	t.Helper()

	raw, err := os.ReadFile(stablePragueModexpFixturePathForFile(t, fileName))
	require.NoError(t, err)

	var fixtures map[string]stablePragueModexpFixtureJSON
	require.NoError(t, json.Unmarshal(raw, &fixtures))

	fixtureJSON, ok := fixtures[fixtureKey]
	require.True(t, ok)
	return fixtureJSON
}

func loadStablePragueModexpFixtureJSON(t *testing.T) stablePragueModexpFixtureJSON {
	t.Helper()
	return loadStablePragueModexpFixtureJSONForFileAndKey(t, stablePragueModexpFixtureFile, stablePragueModexpFixtureKey)
}

func loadStableShanghaiWithdrawalFixtureJSON(t *testing.T) stablePragueModexpFixtureJSON {
	t.Helper()

	raw, err := os.ReadFile(stableShanghaiWithdrawalFixturePath(t))
	require.NoError(t, err)

	var fixtures map[string]stablePragueModexpFixtureJSON
	require.NoError(t, json.Unmarshal(raw, &fixtures))

	fixtureJSON, ok := fixtures[stableShanghaiWithdrawalFixtureKey]
	require.True(t, ok)
	return fixtureJSON
}

func loadStablePragueModexpFixture(t *testing.T) stablePragueModexpFixture {
	t.Helper()
	return loadStablePragueModexpFixtureForFileAndKey(t, stablePragueModexpFixtureFile, stablePragueModexpFixtureKey)
}

func loadStableShanghaiWithdrawalFixture(t *testing.T) stablePragueModexpFixture {
	t.Helper()

	fixtureJSON := loadStableShanghaiWithdrawalFixtureJSON(t)
	genesisExtraData, err := hexutil.Decode(fixtureJSON.GenesisBlockHeader.ExtraData)
	require.NoError(t, err)
	return stablePragueModexpFixture{
		Pre:              decodeStablePragueModexpAlloc(t, fixtureJSON.Pre),
		PostState:        decodeStablePragueModexpAlloc(t, fixtureJSON.PostState),
		GenesisBlockHash: types.HexToHash(fixtureJSON.GenesisBlockHeader.Hash),
		GenesisExtraData: genesisExtraData,
	}
}

func loadStablePragueModexpEntryPointExactGasFixture(t *testing.T) stablePragueModexpFixture {
	t.Helper()
	return loadStablePragueModexpFixtureForFileAndKey(t, stablePragueModexpEntryPointFixtureFile, stablePragueModexpEntryPointExactGasFixtureKey)
}

func loadStablePragueModexpFixtureForFileAndKey(t *testing.T, fileName, fixtureKey string) stablePragueModexpFixture {
	t.Helper()
	fixtureJSON := loadStablePragueModexpFixtureJSONForFileAndKey(t, fileName, fixtureKey)
	var err error
	genesisExtraData, err := hexutil.Decode(fixtureJSON.GenesisBlockHeader.ExtraData)
	require.NoError(t, err)
	fixture := stablePragueModexpFixture{
		Pre:              decodeStablePragueModexpAlloc(t, fixtureJSON.Pre),
		PostState:        decodeStablePragueModexpAlloc(t, fixtureJSON.PostState),
		GenesisBlockHash: types.HexToHash(fixtureJSON.GenesisBlockHeader.Hash),
		GenesisExtraData: genesisExtraData,
	}
	require.NotEmpty(t, fixture.Pre)
	return fixture
}

func loadStablePragueModexpEnginePayloadFixture(t *testing.T) stablePragueModexpEnginePayloadFixture {
	t.Helper()
	return loadStablePragueModexpEnginePayloadFixtureForFileAndKey(t, stablePragueModexpFixtureFile, stablePragueModexpFixtureKey)
}

func loadStableShanghaiWithdrawalEnginePayloadFixture(t *testing.T) *ExecutionPayloadV2 {
	t.Helper()

	fixtureJSON := loadStableShanghaiWithdrawalFixtureJSON(t)
	require.NotEmpty(t, fixtureJSON.EngineNewPayloads)
	call := fixtureJSON.EngineNewPayloads[0]
	require.Equal(t, "2", call.NewPayloadVersion)
	require.Len(t, call.Params, 1)

	var payload ExecutionPayloadV2
	require.NoError(t, json.Unmarshal(call.Params[0], &payload))
	return &payload
}

func loadStablePragueModexpEntryPointExactGasEnginePayloadFixture(t *testing.T) stablePragueModexpEnginePayloadFixture {
	t.Helper()
	return loadStablePragueModexpEnginePayloadFixtureForFileAndKey(t, stablePragueModexpEntryPointFixtureFile, stablePragueModexpEntryPointExactGasFixtureKey)
}

func loadStablePragueModexpEnginePayloadFixtureForFileAndKey(t *testing.T, fileName, fixtureKey string) stablePragueModexpEnginePayloadFixture {
	t.Helper()

	fixtureJSON := loadStablePragueModexpFixtureJSONForFileAndKey(t, fileName, fixtureKey)
	require.NotEmpty(t, fixtureJSON.EngineNewPayloads)
	call := fixtureJSON.EngineNewPayloads[0]
	require.Equal(t, "4", call.NewPayloadVersion)
	require.Len(t, call.Params, 4)

	var payload ExecutionPayloadV4
	require.NoError(t, json.Unmarshal(call.Params[0], &payload))

	var blobVersionedHashes []types.Hash
	require.NoError(t, json.Unmarshal(call.Params[1], &blobVersionedHashes))

	var parentBeaconBlockRoot types.Hash
	require.NoError(t, json.Unmarshal(call.Params[2], &parentBeaconBlockRoot))

	var executionRequests []hexutil.Bytes
	require.NoError(t, json.Unmarshal(call.Params[3], &executionRequests))

	return stablePragueModexpEnginePayloadFixture{
		Payload:                     &payload,
		ExpectedBlobVersionedHashes: blobVersionedHashes,
		ParentBeaconBlockRoot:       parentBeaconBlockRoot,
		ExecutionRequests:           executionRequests,
	}
}

func stablePragueModexpHiveEnv() map[string]string {
	return map[string]string{
		"HIVE_CHAIN_ID":                  "1",
		"HIVE_NETWORK_ID":                "1",
		"HIVE_FORK_HOMESTEAD":            "0",
		"HIVE_FORK_DAO_VOTE":             "1",
		"HIVE_FORK_TANGERINE":            "0",
		"HIVE_FORK_SPURIOUS":             "0",
		"HIVE_FORK_BYZANTIUM":            "0",
		"HIVE_FORK_CONSTANTINOPLE":       "0",
		"HIVE_FORK_PETERSBURG":           "0",
		"HIVE_FORK_ISTANBUL":             "0",
		"HIVE_FORK_BERLIN":               "0",
		"HIVE_FORK_LONDON":               "0",
		"HIVE_FORK_MERGE":                "0",
		"HIVE_TERMINAL_TOTAL_DIFFICULTY": "0",
		"HIVE_SHANGHAI_TIMESTAMP":        "0",
		"HIVE_CANCUN_TIMESTAMP":          "0",
		"HIVE_PRAGUE_TIMESTAMP":          "0",
	}
}

func stableShanghaiWithdrawalHiveEnv() map[string]string {
	return map[string]string{
		"HIVE_CHAIN_ID":                  "1",
		"HIVE_NETWORK_ID":                "1",
		"HIVE_FORK_HOMESTEAD":            "0",
		"HIVE_FORK_DAO_VOTE":             "1",
		"HIVE_FORK_TANGERINE":            "0",
		"HIVE_FORK_SPURIOUS":             "0",
		"HIVE_FORK_BYZANTIUM":            "0",
		"HIVE_FORK_CONSTANTINOPLE":       "0",
		"HIVE_FORK_PETERSBURG":           "0",
		"HIVE_FORK_ISTANBUL":             "0",
		"HIVE_FORK_BERLIN":               "0",
		"HIVE_FORK_LONDON":               "0",
		"HIVE_FORK_MERGE":                "0",
		"HIVE_TERMINAL_TOTAL_DIFFICULTY": "0",
		"HIVE_SHANGHAI_TIMESTAMP":        "0",
	}
}

func stableShanghaiWithdrawalFixturePathForFile(t *testing.T, fileName string) string {
	t.Helper()

	var candidates []string
	if cacheDir, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(
			cacheDir,
			"ethereum-execution-spec-tests",
			"cached_downloads",
			"ethereum",
			"execution-spec-tests",
			"v5.4.0",
			"fixtures_stable",
			"fixtures",
			"blockchain_tests_engine",
			"shanghai",
			"eip4895_withdrawals",
			fileName,
		))
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skipf("stable EEST withdrawal fixture missing; checked: %s", strings.Join(candidates, ", "))
	return ""
}

func decodeStablePragueModexpAlloc(t *testing.T, raw map[string]stablePragueModexpAccountJSON) conf.GenesisAlloc {
	t.Helper()

	alloc := make(conf.GenesisAlloc, len(raw))
	for addrHex, account := range raw {
		genesisAccount := conf.GenesisAccount{
			Balance: account.Balance,
		}
		if account.Code != "" {
			code, err := hexutil.Decode(account.Code)
			require.NoError(t, err)
			genesisAccount.Code = code
		}
		if account.Nonce != "" {
			nonce, err := parseFixtureUint64(account.Nonce)
			require.NoError(t, err)
			genesisAccount.Nonce = nonce
		}
		if len(account.Storage) > 0 {
			genesisAccount.Storage = make(map[types.Hash]types.Hash, len(account.Storage))
			for slotHex, valueHex := range account.Storage {
				genesisAccount.Storage[types.HexToHash(slotHex)] = types.HexToHash(valueHex)
			}
		}
		alloc[types.HexToAddress(addrHex)] = genesisAccount
	}
	return alloc
}

func parseFixtureUint64(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		return strconv.ParseUint(raw[2:], 16, 64)
	}
	return strconv.ParseUint(raw, 10, 64)
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
			WithTableCfg(ethel.ChainTableCfg).
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
