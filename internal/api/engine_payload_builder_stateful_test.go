package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type mockEngineTxPool struct {
	pending map[types.Address][]*transaction.Transaction
}

func (m *mockEngineTxPool) Stop() error { return nil }

func (m *mockEngineTxPool) Has(hash types.Hash) bool {
	return m.GetTx(hash) != nil
}

func (m *mockEngineTxPool) Pending(bool) map[types.Address][]*transaction.Transaction {
	return m.pending
}

func (m *mockEngineTxPool) GetTransaction() ([]*transaction.Transaction, error) {
	flat := make([]*transaction.Transaction, 0)
	for _, txs := range m.pending {
		flat = append(flat, txs...)
	}
	return flat, nil
}

func (m *mockEngineTxPool) GetTx(hash types.Hash) *transaction.Transaction {
	for _, txs := range m.pending {
		for _, tx := range txs {
			if tx != nil && tx.Hash() == hash {
				return tx
			}
		}
	}
	return nil
}

func (m *mockEngineTxPool) AddRemotes([]*transaction.Transaction) []error { return nil }
func (m *mockEngineTxPool) AddLocal(*transaction.Transaction) error { return nil }
func (m *mockEngineTxPool) AddLocals(txs []*transaction.Transaction) []error {
	return make([]error, len(txs))
}

func (m *mockEngineTxPool) Stats() (int, int, int, int) {
	addresses := len(m.pending)
	txs := 0
	for _, list := range m.pending {
		txs += len(list)
	}
	return addresses, txs, 0, 0
}

func (m *mockEngineTxPool) Nonce(types.Address) uint64 { return 0 }

func (m *mockEngineTxPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return m.pending, map[types.Address][]*transaction.Transaction{}
}

func TestForkchoiceUpdatedV1BuildsPayloadFromTxPool(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipient := types.HexToAddress("0x2222222222222222222222222222222222222222")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

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
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{sender: {signedTx}}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	attrs := &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	}

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, attrs)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload.Transactions, 1)
	require.NotZero(t, uint64(payload.GasUsed))
	require.NotEqual(t, hash.EmptyRootHash, payload.ReceiptsRoot)
	require.NotEqual(t, genesisBlock.StateRoot(), payload.StateRoot)

	rebuilt, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(rebuilt, cfg))

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *newPayloadResp.LatestValidHash)
}

func TestForkchoiceUpdatedV2BuildsPayloadWithWithdrawals(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	recipient := types.HexToAddress("0x5555555555555555555555555555555555555555")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      conf.GenesisAlloc{},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
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
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	withdrawals := []*Withdrawal{{
		Index:          1,
		ValidatorIndex: 2,
		Address:        recipient,
		Amount:         3,
	}}
	attrs := &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             2,
			PrevRandao:            types.Hash{0x11},
			SuggestedFeeRecipient: coinbase,
		},
		Withdrawals: withdrawals,
	}

	resp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, attrs)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.NotNil(t, resp.PayloadID)

	payloadResp, err := engine.GetPayloadV2(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, payloadResp.ExecutionPayload)
	require.Len(t, payloadResp.ExecutionPayload.Withdrawals, 1)

	var expectedRoot types.Hash
	err = withCanonicalParentState(db, 0, func(_ kv.Tx, _ state.StateReader, ibs *state.IntraBlockState) error {
		applyExecutionWithdrawals(ibs, withdrawals)
		expectedRoot = ibs.IntermediateRoot()
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedRoot, payloadResp.ExecutionPayload.StateRoot)

	newPayloadResp, err := engine.NewPayloadV2(context.Background(), payloadResp.ExecutionPayload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, payloadResp.ExecutionPayload.BlockHash, *newPayloadResp.LatestValidHash)

	finalResp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payloadResp.ExecutionPayload.BlockHash,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, finalResp.PayloadStatus.Status)

	balance, err := NewBlockChainAPI(api).GetBalance(
		context.Background(),
		avmcommon.BytesToAddress(recipient.Bytes()),
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber),
	)
	require.NoError(t, err)
	require.NotNil(t, balance)
	require.Equal(t, int64(3*params.GWei), balance.ToInt().Int64())
}

func TestNewPayloadV1InvalidStateRootUsesParentAsLatestValidHash(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipient := types.HexToAddress("0x4444444444444444444444444444444444444444")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

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
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{sender: {signedTx}}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload.Transactions, 1)

	payload.StateRoot[0] ^= 0xff
	mutatedBlock, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleBlockHash(mutatedBlock, cfg)

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, headHash, *newPayloadResp.LatestValidHash)
}

func TestNewPayloadV1InvalidPrevRandaoUsesParentAsLatestValidHash(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	prevRandaoContract := types.HexToAddress("0x0000000000000000000000000000000000000316")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				prevRandaoContract: {
					Balance: "0x0",
					Code:    types.Hex2Bytes("444355"),
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)

	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       75_000,
		To:        &prevRandaoContract,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

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
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{sender: {signedTx}}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	attrs := &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	}

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, attrs)
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload.Transactions, 1)

	payload.PrevRandao = types.Hash{0x22}
	rebuilt, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleBlockHash(rebuilt, cfg)

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, headHash, *newPayloadResp.LatestValidHash)
}

func TestNewPayloadV1InvalidPrevRandaoUsesParentAsLatestValidHashAfterMultipleLegacyBlocks(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	prevRandaoContract := types.HexToAddress("0x0000000000000000000000000000000000000316")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				prevRandaoContract: {
					Balance: "0x0",
					Code:    types.Hex2Bytes("444355"),
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)

	txpool := &mockEngineTxPool{}
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
		txspool:       txpool,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	for i := uint64(0); i < 5; i++ {
		tx := transaction.NewTx(&transaction.LegacyTx{
			Nonce:    i,
			GasPrice: uint256.NewInt(7),
			Gas:      75_000,
			To:       &prevRandaoContract,
			Value:    uint256.NewInt(1),
		})
		signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
		require.NoError(t, err)
		txpool.pending = map[types.Address][]*transaction.Transaction{
			sender: {signedTx},
		}

		attrs := &PayloadAttributesV1{
			Timestamp:             hexutil.Uint64(i + 2),
			PrevRandao:            types.Hash{byte(i + 1)},
			SuggestedFeeRecipient: coinbase,
		}
		resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
			HeadBlockHash: headHash,
		}, attrs)
		require.NoError(t, err)
		require.NotNil(t, resp.PayloadID)

		payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
		require.NoError(t, err)
		newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, newPayloadResp.Status)

		finalResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
			HeadBlockHash: payload.BlockHash,
		}, nil)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, finalResp.PayloadStatus.Status)
		headHash = payload.BlockHash
	}

	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    5,
		GasPrice: uint256.NewInt(7),
		Gas:      75_000,
		To:       &prevRandaoContract,
		Value:    uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)
	txpool.pending = map[types.Address][]*transaction.Transaction{
		sender: {signedTx},
	}

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             7,
		PrevRandao:            types.Hash{0x55},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	payload.PrevRandao = types.Hash{0x77}
	rebuilt, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	payload.BlockHash = ethCompatibleBlockHash(rebuilt, cfg)

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, headHash, *newPayloadResp.LatestValidHash)
}

func TestForkchoiceUpdatedV1WithEmptyTxPoolFallsBackToStaticPayload(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")
	allocAddr := types.HexToAddress("0x2222222222222222222222222222222222222222")
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				allocAddr: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
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
		txspool:       &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	attrs := &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	}
	statefulPayload := engine.buildExecutionPayloadV1Stateful(genesisBlock, headHash, attrs)
	require.NotNil(t, statefulPayload)
	require.Len(t, statefulPayload.Transactions, 0)

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, attrs)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.NotNil(t, resp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload.Transactions, 0)
	require.Equal(t, statefulPayload.StateRoot, payload.StateRoot)
	require.Equal(t, statefulPayload.ReceiptsRoot, payload.ReceiptsRoot)

	rebuilt, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)
	require.Equal(t, payload.BlockHash, ethCompatibleBlockHash(rebuilt, cfg))

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *newPayloadResp.LatestValidHash)
}

func TestForkchoiceUpdatedV1BuildsConsecutivePayloadsFromOverlayState(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipient := types.HexToAddress("0x3333333333333333333333333333333333333333")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	txPool := &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}}
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
		txspool:       txPool,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	makeTx := func(nonce uint64) *transaction.Transaction {
		tx := transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			Nonce:     nonce,
			GasTipCap: uint256.NewInt(0),
			GasFeeCap: uint256.NewInt(7),
			Gas:       21_000,
			To:        &recipient,
			Value:     uint256.NewInt(1),
		})
		signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
		require.NoError(t, err)
		return signedTx
	}

	txPool.pending = map[types.Address][]*transaction.Transaction{sender: {makeTx(0)}}
	resp1, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, resp1.PayloadID)
	payload1, err := engine.GetPayloadV1(context.Background(), *resp1.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload1.Transactions, 1)

	newPayloadResp1, err := engine.NewPayloadV1(context.Background(), payload1)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp1.Status)

	finalResp1, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload1.BlockHash,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, finalResp1.PayloadStatus.Status)

	txPool.pending = map[types.Address][]*transaction.Transaction{sender: {makeTx(1)}}
	resp2, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload1.BlockHash,
	}, &PayloadAttributesV1{
		Timestamp:             3,
		PrevRandao:            types.Hash{0x22},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, resp2.PayloadID)
	payload2, err := engine.GetPayloadV1(context.Background(), *resp2.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload2.Transactions, 1)

	newPayloadResp2, err := engine.NewPayloadV1(context.Background(), payload2)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp2.Status)

	finalResp2, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload2.BlockHash,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, finalResp2.PayloadStatus.Status)

	balance, err := NewBlockChainAPI(api).GetBalance(
		context.Background(),
		avmcommon.BytesToAddress(recipient.Bytes()),
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber),
	)
	require.NoError(t, err)
	require.NotNil(t, balance)
	require.Equal(t, int64(2), balance.ToInt().Int64())
}

func TestForkchoiceUpdatedV2BuildsWithdrawalsPayloadOnOverlayHead(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipient := types.HexToAddress("0x4444444444444444444444444444444444444444")
	withdrawalA := types.HexToAddress("0x0000000000000000000000000000000000001000")
	withdrawalB := types.HexToAddress("0x0000000000000000000000000000000000001001")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	txPool := &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}}
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
		txspool:       txPool,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	makeTx := func(nonce uint64) *transaction.Transaction {
		tx := transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			Nonce:     nonce,
			GasTipCap: uint256.NewInt(0),
			GasFeeCap: uint256.NewInt(7),
			Gas:       21_000,
			To:        &recipient,
			Value:     uint256.NewInt(1),
		})
		signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
		require.NoError(t, err)
		return signedTx
	}

	for i := uint64(0); i < 2; i++ {
		txPool.pending = map[types.Address][]*transaction.Transaction{sender: {makeTx(i)}}
		resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
			HeadBlockHash: headHash,
		}, &PayloadAttributesV1{
			Timestamp:             hexutil.Uint64(i + 2),
			PrevRandao:            types.Hash{byte(i + 1)},
			SuggestedFeeRecipient: coinbase,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.PayloadID)

		payload, err := engine.GetPayloadV1(context.Background(), *resp.PayloadID)
		require.NoError(t, err)
		require.Len(t, payload.Transactions, 1)

		newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, newPayloadResp.Status)

		finalResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
			HeadBlockHash: payload.BlockHash,
		}, nil)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, finalResp.PayloadStatus.Status)
		headHash = payload.BlockHash
	}

	txPool.pending = map[types.Address][]*transaction.Transaction{}
	withdrawals := []*Withdrawal{
		{
			Index:          0,
			ValidatorIndex: 0,
			Address:        withdrawalA,
			Amount:         1,
		},
		{
			Index:          1,
			ValidatorIndex: 1,
			Address:        withdrawalB,
			Amount:         2,
		},
	}
	resp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             4,
			PrevRandao:            types.Hash{0x33},
			SuggestedFeeRecipient: coinbase,
		},
		Withdrawals: withdrawals,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	payloadResp, err := engine.GetPayloadV2(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, payloadResp.ExecutionPayload)
	require.Len(t, payloadResp.ExecutionPayload.Transactions, 0)
	require.Len(t, payloadResp.ExecutionPayload.Withdrawals, 2)
	require.NotEqual(t, chain.header.Root, payloadResp.ExecutionPayload.StateRoot)

	newPayloadResp, err := engine.NewPayloadV2(context.Background(), payloadResp.ExecutionPayload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp.Status)
	require.NotNil(t, newPayloadResp.LatestValidHash)
	require.Equal(t, payloadResp.ExecutionPayload.BlockHash, *newPayloadResp.LatestValidHash)
}

func TestForkchoiceUpdatedV2BuildsWithdrawalsPayloadAfterRejectedPendingTx(t *testing.T) {
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
	}
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipient := types.HexToAddress("0x5555555555555555555555555555555555555555")
	withdrawalRecipient := types.HexToAddress("0x0000000000000000000000000000000000001000")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {Balance: "0x3635c9adc5dea00000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)

	txPool := &mockEngineTxPool{pending: map[types.Address][]*transaction.Transaction{}}
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
		txspool:       txPool,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, cfg)
	validTx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedValidTx, err := transaction.SignTx(validTx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)
	txPool.pending = map[types.Address][]*transaction.Transaction{sender: {signedValidTx}}

	resp1, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, resp1.PayloadID)

	payload1, err := engine.GetPayloadV1(context.Background(), *resp1.PayloadID)
	require.NoError(t, err)
	require.Len(t, payload1.Transactions, 1)

	newPayloadResp1, err := engine.NewPayloadV1(context.Background(), payload1)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp1.Status)

	finalResp1, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload1.BlockHash,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, finalResp1.PayloadStatus.Status)
	headHash = payload1.BlockHash

	rejectedTx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     5,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedRejectedTx, err := transaction.SignTx(rejectedTx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)
	txPool.pending = map[types.Address][]*transaction.Transaction{sender: {signedRejectedTx}}

	resp2, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             3,
			PrevRandao:            types.Hash{0x22},
			SuggestedFeeRecipient: coinbase,
		},
		Withdrawals: []*Withdrawal{{
			Index:          0,
			ValidatorIndex: 0,
			Address:        withdrawalRecipient,
			Amount:         1,
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp2.PayloadID)

	payloadResp, err := engine.GetPayloadV2(context.Background(), *resp2.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, payloadResp.ExecutionPayload)
	require.Len(t, payloadResp.ExecutionPayload.Transactions, 0)
	require.Len(t, payloadResp.ExecutionPayload.Withdrawals, 1)
	require.NotEqual(t, chain.header.Root, payloadResp.ExecutionPayload.StateRoot)

	newPayloadResp2, err := engine.NewPayloadV2(context.Background(), payloadResp.ExecutionPayload)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, newPayloadResp2.Status)
	require.NotNil(t, newPayloadResp2.LatestValidHash)
	require.Equal(t, payloadResp.ExecutionPayload.BlockHash, *newPayloadResp2.LatestValidHash)
}
