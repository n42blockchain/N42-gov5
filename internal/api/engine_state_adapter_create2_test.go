package api

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	vmcore "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestEngineStateAdapterAcceptsCreate2RecreateFixture(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	const (
		creatorCodeHex = "36600060003760003660006000f5"
		initcodeHex    = "610010600081600b8239f334600014600c5734600055005b6000ff"
	)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	creatorAddr := types.HexToAddress("0x10000000000000000000000000000000000000c2")
	creatorCode := types.Hex2Bytes(creatorCodeHex)
	initcode := types.Hex2Bytes(initcodeHex)
	createdAddr := crypto.CreateAddress2(creatorAddr, [32]byte{}, crypto.Keccak256(initcode))

	for _, tc := range []struct {
		name       string
		cfg        *params.ChainConfig
		wantBlock1 uint64
		wantBlock2 uint64
		wantBlock3 uint64
	}{
		{
			name:       "Paris",
			cfg:        create2RecreateFixtureChainConfig(false),
			wantBlock1: 99744,
			wantBlock2: 74625,
			wantBlock3: 56618,
		},
		{
			name:       "Shanghai",
			cfg:        create2RecreateFixtureChainConfig(true),
			wantBlock1: 99746,
			wantBlock2: 74625,
			wantBlock3: 56620,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := memdb.NewTestDB(t)
			writeCreate2RecreateGenesis(t, db, tc.cfg, sender, creatorAddr, creatorCode)

			createTx := signCreate2RecreateLegacyTx(t, tc.cfg, senderKey, 1, 0, &creatorAddr, 0, initcode)
			setStorageTx := signCreate2RecreateLegacyTx(t, tc.cfg, senderKey, 1, 1, &createdAddr, 1, nil)
			usedGas, receipts := executeCreate2RecreateBlock(t, db, tc.cfg, 1, createTx, setStorageTx)
			require.Equal(t, tc.wantBlock1, usedGas)
			require.Len(t, receipts, 2)
			require.Equal(t, block.ReceiptStatusSuccessful, receipts[0].Status)
			require.Equal(t, block.ReceiptStatusSuccessful, receipts[1].Status)

			destructTx := signCreate2RecreateLegacyTx(t, tc.cfg, senderKey, 2, 2, &createdAddr, 0, nil)
			sendFundsTx := signCreate2RecreateLegacyTx(t, tc.cfg, senderKey, 2, 3, &createdAddr, 1, nil)
			usedGas, receipts = executeCreate2RecreateBlock(t, db, tc.cfg, 2, destructTx, sendFundsTx)
			require.Equal(t, tc.wantBlock2, usedGas)
			require.Len(t, receipts, 2)
			require.Equal(t, block.ReceiptStatusSuccessful, receipts[0].Status)
			require.Equal(t, block.ReceiptStatusSuccessful, receipts[1].Status)

			err := db.View(context.Background(), func(tx kv.Tx) error {
				reader := state.NewPlainState(tx, 3)
				ibs := state.New(reader)
				require.True(t, ibs.Exist(createdAddr))
				require.Equal(t, uint64(0), ibs.GetNonce(createdAddr))
				require.Equal(t, uint64(1), ibs.GetBalance(createdAddr).Uint64())
				require.Equal(t, 0, ibs.GetCodeSize(createdAddr))
				require.False(t, ibs.HasNonEmptyStorage(createdAddr))
				require.True(t, account.IsEmptyCodeHash(ibs.GetCodeHash(createdAddr)))
				return nil
			})
			require.NoError(t, err)

			reCreateTx := signCreate2RecreateLegacyTx(t, tc.cfg, senderKey, 3, 4, &creatorAddr, 0, initcode)
			usedGas, receipts = executeCreate2RecreateBlock(t, db, tc.cfg, 3, reCreateTx)
			require.Len(t, receipts, 1)
			require.Equal(t, block.ReceiptStatusSuccessful, receipts[0].Status)
			require.Equal(t, tc.wantBlock3, usedGas)
		})
	}
}

func create2RecreateFixtureChainConfig(shanghai bool) *params.ChainConfig {
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
	if shanghai {
		cfg.ShanghaiBlock = big.NewInt(0)
	}
	return cfg
}

func writeCreate2RecreateGenesis(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, sender, creator types.Address, creatorCode []byte) {
	t.Helper()

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				sender: {
					Balance: "0x3635c9adc5dea00000",
				},
				creator: {
					Balance: "0x0",
					Code:    creatorCode,
					Nonce:   1,
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  0,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   types.Address{},
		},
	}

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		_, _, err := genesis.Write(tx)
		return err
	})
	require.NoError(t, err)
}

func signCreate2RecreateLegacyTx(t *testing.T, cfg *params.ChainConfig, key *ecdsa.PrivateKey, blockNum uint64, nonce uint64, to *types.Address, value uint64, data []byte) *transaction.Transaction {
	t.Helper()

	signer := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(int64(blockNum)), blockNum)
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(7),
		Gas:      100_000,
		To:       to,
		Value:    uint256.NewInt(value),
		Data:     append([]byte(nil), data...),
	})
	signedTx, err := transaction.SignTx(tx, signer, key)
	require.NoError(t, err)

	encoded, err := transaction.EncodeEthereumTransaction(signedTx)
	require.NoError(t, err)

	decodedTx, err := transaction.DecodeEthereumTransaction(encoded)
	require.NoError(t, err)
	return decodedTx
}

func executeCreate2RecreateBlock(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, blockNum uint64, txs ...*transaction.Transaction) (uint64, block.Receipts) {
	t.Helper()

	var (
		usedGas  uint64
		receipts block.Receipts
	)
	coinbase := types.HexToAddress("0x10000000000000000000000000000000000000cb")

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		reader := state.NewPlainState(tx, blockNum)
		writer := state.NewPlainStateWriter(tx, tx, blockNum)
		ibs := state.New(reader)
		ibs.BeginWriteCodes()

		header := &block.Header{
			Number:     uint256.NewInt(blockNum),
			GasLimit:   30_000_000,
			Time:       blockNum,
			BaseFee:    uint256.NewInt(7),
			Difficulty: uint256.NewInt(0),
			Coinbase:   coinbase,
		}

		if err := internalcore.ProcessExecutionBlockStart(nil, cfg, ibs, header, &apiTestEngine{}); err != nil {
			return err
		}

		gasPool := new(common.GasPool).AddGas(header.GasLimit)
		for i, tx := range txs {
			ibs.Prepare(tx.Hash(), types.Hash{}, i)
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
			if err != nil {
				return err
			}
			receipts = append(receipts, receipt)
		}

		if _, err := internalcore.ProcessExecutionBlockEnd(receipts, cfg, ibs, header, &apiTestEngine{}); err != nil {
			return err
		}

		if err := ibs.CommitBlock(cfg.RulesWithTimestamp(blockNum, header.Time), writer); err != nil {
			return err
		}
		if err := writer.WriteChangeSets(); err != nil {
			return err
		}
		return writer.WriteHistory()
	})
	require.NoError(t, err)

	return usedGas, receipts
}
