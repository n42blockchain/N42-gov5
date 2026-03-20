package api

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestEngineAPIv4AcceptsOsakaSetCodeDelegationPayload(t *testing.T) {
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

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				authSigner: {
					Balance: "0x0",
				},
				target: {
					Balance: "0x0",
					Code:    types.Hex2Bytes("60011e60005560021e6001557001000000000000000000000000000000001e6002557f80000000000000000000000000000000000000000000000000000000000000001e60035500"),
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
	require.NotNil(t, genesisBlock)

	auth := signEngineTestAuthorization(t, authKey, &transaction.Authorization{
		ChainID: 1,
		Address: target,
		Nonce:   0,
	})

	tx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       200_000,
		To:        &authSigner,
		Value:     uint256.NewInt(0),
		AuthList:  transaction.AuthorizationList{auth},
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

	rawTx, err := transaction.EncodeEthereumTransaction(signedTx)
	require.NoError(t, err)

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       2,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
		Coinbase:   coinbase,
	}
	usedGas := engineTestUsedGas(t, db, cfg, header, signedTx)
	require.NotZero(t, usedGas)
	require.Equal(t, uint64(114_544), usedGas)

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
	beaconRoot := types.Hash{0x77}
	payload := &ExecutionPayloadV4{
		ParentHash:    parentHash,
		FeeRecipient:  coinbase,
		StateRoot:     types.Hash{0x01},
		ReceiptsRoot:  types.Hash{0x02},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x03},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(header.GasLimit),
		GasUsed:       hexutil.Uint64(usedGas),
		Timestamp:     hexutil.Uint64(header.Time),
		BaseFeePerGas: hexutil.Uint64(header.BaseFee.Uint64()),
		Transactions:  []hexutil.Bytes{rawTx},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)

	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, &beaconRoot, nil)
	require.NoError(t, err)

	requestsHash := executionRequestsHash(nil)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &beaconRoot, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
}

func TestEngineAPIv4RejectsPragueWithdrawalSystemCallFailure(t *testing.T) {
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

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	receiver := types.HexToAddress("0x2222222222222222222222222222222222222222")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				vm2.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    []byte{0xfe},
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

	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(7),
		Gas:      21_000,
		To:       &receiver,
		Value:    uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       2,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	usedGas := engineTestUsedGas(t, db, cfg, header, signedTx)
	require.Equal(t, uint64(21_000), usedGas)
	header.GasUsed = usedGas

	parentHash := ethCompatibleBlockHash(genesisBlock, cfg)
	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     db,
	}
	api := &API{
		bc:          chain,
		db:          db,
		engine:      &apiTestEngine{},
		chainConfig: cfg,
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	blk := block.NewBlock(header, []*transaction.Transaction{signedTx})

	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, nil, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "system call failed to execute")
}

func TestEngineAPIv4RejectsPragueOutOfOrderWithdrawalRequests(t *testing.T) {
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

	reqA := &vm2.WithdrawalRequest{
		SourceAddress: types.HexToAddress("0x1000000000000000000000000000000000000001"),
		Amount:        0,
	}
	reqA.ValidatorPubkey[47] = 0x01
	reqB := &vm2.WithdrawalRequest{
		SourceAddress: types.HexToAddress("0x1000000000000000000000000000000000000002"),
		Amount:        0,
	}
	reqB.ValidatorPubkey[47] = 0x02

	actualSerialized := append(reqA.Serialize(), reqB.Serialize()...)
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				vm2.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    buildReturnDataContract(actualSerialized),
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
		bc:          chain,
		db:          db,
		engine:      &apiTestEngine{},
		chainConfig: cfg,
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       2,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	blk := block.NewBlock(header, nil)

	expectedRequests := []hexutil.Bytes{
		append([]byte{WithdrawalRequestType}, append(reqB.Serialize(), reqA.Serialize()...)...),
	}
	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, nil, expectedRequests)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid requests hash")
}

func TestEngineAPIv4AcceptsPragueWithdrawalSystemCallAtGasLimit(t *testing.T) {
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
				vm2.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    buildExactGasSystemContract(30_000_000),
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
		bc:          chain,
		db:          db,
		engine:      &apiTestEngine{},
		chainConfig: cfg,
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       2,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	blk := block.NewBlock(header, nil)

	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, nil, nil)
	require.NoError(t, err)
}

func TestProcessPragueSystemCallsMatchesModifiedWithdrawalContractFixture(t *testing.T) {
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

	serializedRequests := make([][]byte, 0, 15)
	for i := 0; i < 15; i++ {
		req := &vm2.WithdrawalRequest{
			Amount: 0,
		}
		req.ValidatorPubkey[47] = byte(i + 1)
		serialized := req.Serialize()
		serializedRequests = append(serializedRequests, serialized)
	}

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				vm2.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    buildMacroMstoreReturnContract(serializedRequests),
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

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       2,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}

	var (
		rawRet   []byte
		requests []hexutil.Bytes
	)
	err = withCanonicalParentState(db, 0, func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error {
		raw, err := internalcore.SysCallContract(vm2.WithdrawalRequestsAddress, nil, *cfg, ibs, header, &apiTestEngine{})
		if err != nil {
			return err
		}
		rawRet = raw

		grouped, err := internalcore.ProcessPragueSystemCalls(cfg, ibs, header, &apiTestEngine{})
		if err != nil {
			return err
		}
		requests = grouped
		return nil
	})
	require.NoError(t, err)

	expectedOpaquePayload := buildModifiedWithdrawalFixtureReturnPayload(serializedRequests)
	require.Equal(t, expectedOpaquePayload, rawRet)
	require.Equal(t, []hexutil.Bytes{
		append([]byte{WithdrawalRequestType}, expectedOpaquePayload...),
	}, requests)
}

func TestValidateExecutionRequestsAcceptsOpaqueWithdrawalPayloadWhenLegacyArraysOmitted(t *testing.T) {
	serializedRequests := make([][]byte, 0, 15)
	for i := 0; i < 15; i++ {
		req := &vm2.WithdrawalRequest{}
		req.ValidatorPubkey[47] = byte(i + 1)
		serializedRequests = append(serializedRequests, req.Serialize())
	}
	requests := []hexutil.Bytes{
		append([]byte{WithdrawalRequestType}, buildModifiedWithdrawalFixtureReturnPayload(serializedRequests)...),
	}
	payload := &ExecutionPayloadV4{}
	require.NoError(t, validateExecutionRequests(requests, payload))
}

func TestEngineAPIv4AcceptsPragueOpaqueWithdrawalRequests(t *testing.T) {
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

	serializedRequests := make([][]byte, 0, 15)
	for i := 0; i < 15; i++ {
		req := &vm2.WithdrawalRequest{}
		req.ValidatorPubkey[47] = byte(i + 1)
		serializedRequests = append(serializedRequests, req.Serialize())
	}
	executionRequests := []hexutil.Bytes{
		append([]byte{WithdrawalRequestType}, buildModifiedWithdrawalFixtureReturnPayload(serializedRequests)...),
	}

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				vm2.WithdrawalRequestsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    buildMacroMstoreReturnContract(serializedRequests),
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

	beaconRoot := types.Hash{0x77}
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
	require.Equal(t, PayloadStatusValid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
}

func TestEngineAPIv4AcceptsBeaconRootTimestampPayload(t *testing.T) {
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

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	caller := types.HexToAddress("0x1000000000000000000000000000000000000001")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				caller: {
					Balance: "0x0",
					Code:    buildBeaconRootCallerContract(),
				},
				params.BeaconRootsAddress: {
					Balance: "0x0",
					Nonce:   1,
					Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604d57602036146024575f5ffd5b5f35801560495762001fff810690815414603c575f5ffd5b62001fff01545f5260205ff35b5f5ffd5b62001fff42064281555f359062001fff015500"),
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
	require.NotNil(t, genesisBlock)

	var timestampArg types.Hash
	binary.BigEndian.PutUint64(timestampArg[24:], 12)
	beaconRoot := types.Hash{0x44}
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       1_000_000,
		To:        &caller,
		Value:     uint256.NewInt(0),
		Data:      timestampArg[:],
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)
	rawTx, err := transaction.EncodeEthereumTransaction(signedTx)
	require.NoError(t, err)

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       12,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
		Coinbase:   coinbase,
		GasUsed:    0,
	}
	usedGasWithoutBeaconRoot := engineTestUsedGasWithBeaconRoot(t, db, cfg, header, signedTx, nil)
	usedGas := engineTestUsedGasWithBeaconRoot(t, db, cfg, header, signedTx, &beaconRoot)
	require.Greater(t, usedGas, usedGasWithoutBeaconRoot)
	require.Greater(t, usedGas, uint64(100_000))

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
	payload := &ExecutionPayloadV4{
		ParentHash:    parentHash,
		FeeRecipient:  coinbase,
		StateRoot:     types.Hash{0x01},
		ReceiptsRoot:  types.Hash{0x02},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    types.Hash{0x03},
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(header.GasLimit),
		GasUsed:       hexutil.Uint64(usedGas),
		Timestamp:     hexutil.Uint64(header.Time),
		BaseFeePerGas: hexutil.Uint64(header.BaseFee.Uint64()),
		Transactions:  []hexutil.Bytes{rawTx},
		Withdrawals:   []*Withdrawal{},
		BlobGasUsed:   hexUint64Ptr(0),
		ExcessBlobGas: hexUint64Ptr(0),
	}
	blk, err := executionPayloadV4ToBlock(payload)
	require.NoError(t, err)

	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash, &beaconRoot, nil)
	require.NoError(t, err)

	requestsHash := executionRequestsHash(nil)
	payload.BlockHash = ethCompatibleEngineBlockHash(blk, cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   &beaconRoot,
		requestsHash:       &requestsHash,
	})

	resp, err := engine.NewPayloadV4(context.Background(), payload, nil, &beaconRoot, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, payload.BlockHash, *resp.LatestValidHash)
}

func TestEnginePayloadValidationAcceptsOsakaCreate2SelfdestructFixture(t *testing.T) {
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

	sender := types.HexToAddress("0xc2ea0e26d784793578f0052a18fa4ac643e7daad")
	coinbase := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	recipientA := types.HexToAddress("0xcc95f856b89176f4fdb6457742a312b7cd76e8d6")
	recipientB := types.HexToAddress("0xcc95f856b89176f4fdb6457742a312b7cd76e9d6")
	initcodeCarrier := types.HexToAddress("0xcc95f856b89176f4fdb6457742a312b7cd76ead6")
	destroyedAddr := types.HexToAddress("0x954c9d28b2130f7dbad8fcbd0c89d35facd734f5")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				recipientA: {
					Balance: "0x0",
					Nonce:   1,
					Code:    types.Hex2Bytes("6000600055"),
					Storage: map[types.Hash]types.Hash{
						{}: types.BytesToHash([]byte{0x01}),
					},
				},
				recipientB: {
					Balance: "0x0",
					Nonce:   1,
					Code:    types.Hex2Bytes("6000600055"),
					Storage: map[types.Hash]types.Hash{
						{}: types.BytesToHash([]byte{0x01}),
					},
				},
				initcodeCarrier: {
					Balance: "0x0",
					Nonce:   1,
					Code: types.Hex2Bytes(
						"61003d600081600b8239f3600160005401600055600160003514600e58015760003560005260085801565b306000525b600060005114600c580157600051ff60055801565b005b00",
					),
				},
			},
			Number:     0,
			GasLimit:   90_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
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

	rawTx, err := hexutil.Decode("0xf902dc800a8307a120800fb9028f60486000600073cc95f856b89176f4fdb6457742a312b7cd76ead63c6000604860006000f560005573954c9d28b2130f7dbad8fcbd0c89d35facd734f53b60015573954c9d28b2130f7dbad8fcbd0c89d35facd734f53f60025573954c9d28b2130f7dbad8fcbd0c89d35facd734f56000526000600060206000600073954c9d28b2130f7dbad8fcbd0c89d35facd734f545f160035573954c9d28b2130f7dbad8fcbd0c89d35facd734f53160045573cc95f856b89176f4fdb6457742a312b7cd76e8d66000526000600060206000600173954c9d28b2130f7dbad8fcbd0c89d35facd734f545f160055573954c9d28b2130f7dbad8fcbd0c89d35facd734f53160065573cc95f856b89176f4fdb6457742a312b7cd76e9d66000526000600060206000600273954c9d28b2130f7dbad8fcbd0c89d35facd734f545f160075573954c9d28b2130f7dbad8fcbd0c89d35facd734f53160085573954c9d28b2130f7dbad8fcbd0c89d35facd734f56000526000600060206000600373954c9d28b2130f7dbad8fcbd0c89d35facd734f545f160095573954c9d28b2130f7dbad8fcbd0c89d35facd734f531600a5573cc95f856b89176f4fdb6457742a312b7cd76e8d66000526000600060206000600473954c9d28b2130f7dbad8fcbd0c89d35facd734f545f1600b5573954c9d28b2130f7dbad8fcbd0c89d35facd734f531600c5573cc95f856b89176f4fdb6457742a312b7cd76e9d66000526000600060206000600573954c9d28b2130f7dbad8fcbd0c89d35facd734f545f1600d5573954c9d28b2130f7dbad8fcbd0c89d35facd734f531600e5573954c9d28b2130f7dbad8fcbd0c89d35facd734f53b600f5573954c9d28b2130f7dbad8fcbd0c89d35facd734f53f60105560016048f325a06125638b508bc2fd5b15018c74b8a2b21dfe35d00d668fb95873b25caffe48339fced4100313d502fada029e0631b354c003cb5f326fbd3fd69e854e133af090")
	require.NoError(t, err)
	tx, err := transaction.DecodeEthereumTransaction(rawTx)
	require.NoError(t, err)

	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   90_000_000,
		Time:       1_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
		Coinbase:   coinbase,
	}

	var usedGas uint64
	err = withCanonicalParentState(db, 0, func(_ kv.Tx, _ state.StateReader, ibs *state.IntraBlockState) error {
		gasPool := new(common.GasPool).AddGas(header.GasLimit)
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
			vm2.Config{},
		)
		require.NoError(t, err)
		require.NotNil(t, receipt)
		require.Equal(t, block.ReceiptStatusSuccessful, receipt.Status)
		require.Equal(t, uint64(460_867), usedGas)
		require.Equal(t, uint64(5), ibs.GetBalance(recipientA).Uint64())
		require.Equal(t, uint64(7), ibs.GetBalance(recipientB).Uint64())
		require.False(t, ibs.Exist(destroyedAddr))
		require.False(t, ibs.HasSelfdestructed(destroyedAddr))
		require.True(t, ibs.GetBalance(destroyedAddr).IsZero())
		return nil
	})
	require.NoError(t, err)
}

func signEngineTestAuthorization(t *testing.T, key *ecdsa.PrivateKey, auth *transaction.Authorization) *transaction.Authorization {
	t.Helper()

	hash := auth.SigningHash()
	sig, err := crypto.Sign(hash[:], key)
	require.NoError(t, err)

	auth.R = uint256.NewInt(0).SetBytes(sig[:32])
	auth.S = uint256.NewInt(0).SetBytes(sig[32:64])
	auth.V = uint256.NewInt(uint64(sig[64]))
	return auth
}

func engineTestUsedGas(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, tx *transaction.Transaction) uint64 {
	return engineTestUsedGasWithBeaconRoot(t, db, cfg, header, tx, nil)
}

func engineTestUsedGasWithBeaconRoot(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, tx *transaction.Transaction, beaconRoot *types.Hash) uint64 {
	t.Helper()

	var usedGas uint64
	err := withCanonicalParentState(db, 0, func(_ kv.Tx, _ state.StateReader, ibs *state.IntraBlockState) error {
		require.NoError(t, internalcore.ProcessBeaconBlockRoot(beaconRoot, cfg, ibs, header, &apiTestEngine{}))
		gasPool := new(common.GasPool).AddGas(header.GasLimit)
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
			vm2.Config{},
		)
		require.NoError(t, err)
		require.NotNil(t, receipt)
		require.Equal(t, block.ReceiptStatusSuccessful, receipt.Status)
		return nil
	})
	require.NoError(t, err)
	return usedGas
}

func buildBeaconRootCallerContract() []byte {
	code := make([]byte, 0, 96)
	code = append(code, byte(vm2.CALLDATASIZE), byte(vm2.PUSH0), byte(vm2.PUSH1), 0x20, byte(vm2.CALLDATACOPY))
	code = append(code, byte(vm2.PUSH1), 0x20)
	code = append(code, byte(vm2.PUSH0))
	code = append(code, byte(vm2.CALLDATASIZE))
	code = append(code, byte(vm2.PUSH1), 0x20)
	code = append(code, byte(vm2.PUSH0))
	code = append(code, byte(vm2.PUSH20))
	code = append(code, params.BeaconRootsAddress[:]...)
	code = append(code, byte(vm2.PUSH3), 0x01, 0x86, 0xa0) // 100_000 gas
	code = append(code, byte(vm2.CALL))
	code = append(code, byte(vm2.PUSH0), byte(vm2.SSTORE))
	code = append(code, byte(vm2.PUSH0), byte(vm2.MLOAD), byte(vm2.PUSH1), 0x01, byte(vm2.SSTORE))
	code = append(code, byte(vm2.RETURNDATASIZE), byte(vm2.PUSH1), 0x02, byte(vm2.SSTORE))
	code = append(code, byte(vm2.RETURNDATASIZE), byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.RETURNDATACOPY))
	code = append(code, byte(vm2.PUSH0), byte(vm2.MLOAD), byte(vm2.PUSH1), 0x03, byte(vm2.SSTORE))
	code = append(code, byte(vm2.STOP))
	return code
}

func buildReturnDataContract(data []byte) []byte {
	if len(data) == 0 {
		return []byte{byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.RETURN)}
	}
	code := []byte{
		byte(vm2.PUSH2), byte(len(data) >> 8), byte(len(data)),
		byte(vm2.PUSH2), 0x00, 0x0d,
		byte(vm2.PUSH0),
		byte(vm2.CODECOPY),
		byte(vm2.PUSH2), byte(len(data) >> 8), byte(len(data)),
		byte(vm2.PUSH0),
		byte(vm2.RETURN),
	}
	return append(code, data...)
}

func buildExactGasSystemContract(maxGas uint64) []byte {
	const pushGas = 3
	perStorageGas := params.SstoreSetGasEIP2200 + params.ColdSloadCostEIP2929 + (pushGas * 2)
	writeCount := maxGas / perStorageGas
	remainder := maxGas % perStorageGas

	code := make([]byte, 0, int(writeCount*6+remainder+1))
	for i := uint64(0); i < writeCount; i++ {
		code = appendPushUint(code, 1)
		code = appendPushUint(code, i)
		code = append(code, byte(vm2.SSTORE))
	}
	for i := uint64(0); i < remainder; i++ {
		code = append(code, byte(vm2.JUMPDEST))
	}
	return append(code, byte(vm2.STOP))
}

func appendPushUint(code []byte, value uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	first := 0
	for first < len(buf)-1 && buf[first] == 0 {
		first++
	}
	width := len(buf) - first
	code = append(code, byte(vm2.PUSH1)+byte(width)-1)
	return append(code, buf[first:]...)
}

func buildMacroMstoreReturnContract(chunks [][]byte) []byte {
	code := make([]byte, 0, len(chunks)*96)
	offset := 0
	for _, chunk := range chunks {
		offset += len(chunk)
		code = appendMacroMstore(code, chunk, offset)
	}
	code = append(code, byte(vm2.MSIZE))
	code = appendPushUint(code, 0)
	code = append(code, byte(vm2.RETURN))
	return code
}

func appendMacroMstore(code []byte, data []byte, offset int) []byte {
	for i := 0; i < len(data); {
		end := i + 32
		if end > len(data) {
			end = len(data)
		}
		chunk := data[i:end]
		if len(chunk) == 32 {
			code = appendPushBytesExact(code, chunk)
			code = appendPushUint(code, uint64(offset))
			code = append(code, byte(vm2.MSTORE))
		} else {
			code = appendPushUint(code, uint64(offset))
			code = append(code, byte(vm2.MLOAD))
			code = appendPushBytesExact(code, repeatedByte(0xff, 32-len(chunk)))
			code = append(code, byte(vm2.AND))
			padded := make([]byte, 32)
			copy(padded, chunk)
			code = appendPushBytesExact(code, padded)
			code = append(code, byte(vm2.OR))
			code = appendPushUint(code, uint64(offset))
			code = append(code, byte(vm2.MSTORE))
		}
		offset += len(chunk)
		i = end
	}
	return code
}

func appendPushBytesExact(code []byte, value []byte) []byte {
	if len(value) == 0 {
		return append(code, byte(vm2.PUSH0))
	}
	code = append(code, byte(vm2.PUSH1)+byte(len(value))-1)
	return append(code, value...)
}

func repeatedByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func buildModifiedWithdrawalFixtureReturnPayload(serializedRequests [][]byte) []byte {
	maxTouched := 0
	offset := 0
	for _, serialized := range serializedRequests {
		offset += len(serialized)
		cursor := offset
		for i := 0; i < len(serialized); {
			end := i + 32
			if end > len(serialized) {
				end = len(serialized)
			}
			if touched := cursor + 32; touched > maxTouched {
				maxTouched = touched
			}
			cursor += end - i
			i = end
		}
	}
	raw := make([]byte, 32*((maxTouched+31)/32))
	offset = 76
	for _, serialized := range serializedRequests {
		copy(raw[offset:], serialized)
		offset += len(serialized)
	}
	return raw
}
