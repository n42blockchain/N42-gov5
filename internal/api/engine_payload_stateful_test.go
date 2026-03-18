package api

import (
	"context"
	"crypto/ecdsa"
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

	err = engine.blobAPI().v1().validatePayloadExecution(blk, parentHash)
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
	t.Helper()

	var usedGas uint64
	err := withCanonicalParentState(db, 0, func(_ kv.Tx, _ state.StateReader, ibs *state.IntraBlockState) error {
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
