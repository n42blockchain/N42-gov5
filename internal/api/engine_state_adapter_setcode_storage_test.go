package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestWriteEnginePayloadBlockRoundTripsSetCodeTx(t *testing.T) {
	db := memdb.NewTestDB(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")

	auth := signEngineTestAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
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

	header := &block.Header{
		ParentHash:  types.Hash{0x01},
		Number:      uint256.NewInt(1),
		GasLimit:    30_000_000,
		GasUsed:     123_456,
		Time:        2,
		BaseFee:     uint256.NewInt(7),
		Difficulty:  uint256.NewInt(0),
		Coinbase:    types.HexToAddress("0x9999999999999999999999999999999999999999"),
		Root:        types.Hash{0x11},
		ReceiptHash: types.Hash{0x22},
		Bloom:       block.BytesToBloom(make([]byte, 256)),
	}
	blk := block.NewBlock(header, []*transaction.Transaction{signedTx}).(*block.Block)

	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := writeEnginePayloadBlock(tx, blk); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, blk.Hash(), 1); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, blk.Hash())
		return rawdb.WriteHeadHeaderHash(tx, blk.Hash())
	})
	require.NoError(t, err)

	err = db.View(context.Background(), func(tx kv.Tx) error {
		readBlock, err := rawdb.ReadBlockByHash(tx, blk.Hash())
		require.NoError(t, err)
		require.NotNil(t, readBlock)
		require.Len(t, readBlock.Transactions(), 1)
		require.EqualValues(t, transaction.SetCodeTxType, readBlock.Transactions()[0].Type())
		require.Equal(t, blk.Hash(), readBlock.Hash())
		return nil
	})
	require.NoError(t, err)

	adapter := NewEngineStateAdapter(db, nil, &params.ChainConfig{PragueTime: big.NewInt(0)}, &apiTestEngine{})
	head := adapter.CurrentHead()
	require.NotNil(t, head)
	require.Len(t, head.Transactions(), 1)
	require.EqualValues(t, transaction.SetCodeTxType, head.Transactions()[0].Type())
}
