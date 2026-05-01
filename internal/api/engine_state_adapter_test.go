package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestEngineStateAdapterRejectsInvalidPrevRandaoOnLegacyPayload(t *testing.T) {
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

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	prevRandaoContract := types.HexToAddress("0x0000000000000000000000000000000000000316")
	coinbase := types.Address{}

	writeGenesis := func(t *testing.T, db kv.RwDB) *block.Block {
		t.Helper()

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
		return genesisBlock
	}

	buildDB := memdb.NewTestDB(t)
	genesisBlock := writeGenesis(t, buildDB)

	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(7),
		Gas:      75_000,
		To:       &prevRandaoContract,
		Value:    uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, transaction.NewLondonSigner(big.NewInt(1)), senderKey)
	require.NoError(t, err)

	txpool := &mockEngineTxPool{
		pending: map[types.Address][]*transaction.Transaction{
			sender: {signedTx},
		},
	}
	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     buildDB,
	}
	api := &API{
		bc:            chain,
		db:            buildDB,
		engine:        &apiTestEngine{},
		txspool:       txpool,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	genesisHash := ethCompatibleBlockHash(genesisBlock, cfg)
	buildResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: genesisHash,
	}, &PayloadAttributesV1{
		Timestamp:             hexutil.Uint64(2),
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: coinbase,
	})
	require.NoError(t, err)
	require.NotNil(t, buildResp.PayloadID)

	payload, err := engine.GetPayloadV1(context.Background(), *buildResp.PayloadID)
	require.NoError(t, err)

	validBlock, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)

	probeDB := memdb.NewTestDB(t)
	_ = writeGenesis(t, probeDB)
	err = probeDB.Update(context.Background(), func(tx kv.RwTx) error {
		reader := state.NewPlainState(tx, uint64(payload.BlockNumber))
		ibs := state.New(reader)
		ethel.SetupStateRootComputer(tx, ibs)
		blockHashFunc := func(n uint64) types.Hash {
			h, err := rawdb.ReadCanonicalHash(tx, n)
			if err != nil {
				return types.Hash{}
			}
			return h
		}
		_, err := ethel.ProcessBlock(cfg, &apiTestEngine{}, validBlock.(*block.Block).Header().(*block.Header), validBlock.Transactions(), nil, nil, ibs, blockHashFunc, nil)
		require.NoError(t, err)
		require.Equal(t, payload.StateRoot, ibs.IntermediateRoot())
		return nil
	})
	require.NoError(t, err)

	validDB := memdb.NewTestDB(t)
	_ = writeGenesis(t, validDB)
	valid, stateRoot, err := NewEngineStateAdapter(validDB, nil, cfg, &apiTestEngine{}).ExecutePayload(validBlock.(*block.Block))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, payload.StateRoot, stateRoot)

	invalidPayload := *payload
	invalidPayload.PrevRandao = types.Hash{0x22}
	invalidDB := memdb.NewTestDB(t)
	_ = writeGenesis(t, invalidDB)
	invalidBlock, err := executionPayloadV1ToBlock(&invalidPayload)
	require.NoError(t, err)
	valid, stateRoot, err = NewEngineStateAdapter(invalidDB, nil, cfg, &apiTestEngine{}).ExecutePayload(invalidBlock.(*block.Block))
	require.NoError(t, err)
	require.False(t, valid)
	require.NotEqual(t, payload.StateRoot, stateRoot)
}
