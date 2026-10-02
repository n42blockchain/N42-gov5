package api

// apix_transaction_api_test.go exercises api_transaction.go's read paths
// (GetTransactionCount, GetTransactionReceipt, GetTransactionByHash,
// GetTransactionByBlockHashAndIndex, GetBlockTransactionCountByHash) and
// SendRawTransaction, against the real executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestTransactionAPIGetTransactionCountAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	addr := *avmtypes.FromastAddress(&fx.Senders[0])
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	nonce, err := txAPI.GetTransactionCount(context.Background(), addr, latest)
	require.NoError(t, err)
	require.NotNil(t, nonce)
	// Sender 0 signed the block-1 transfer, the block-4 create and the
	// block-5 call: 3 transactions, so its nonce at head is 3.
	require.Equal(t, uint64(3), uint64(*nonce))

	pending := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber)
	nonce, err = txAPI.GetTransactionCount(context.Background(), addr, pending)
	require.NoError(t, err)
	require.NotNil(t, nonce)
}

func TestTransactionAPIGetTransactionReceiptAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	fields, err := txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(fx.CallTx.Hash()))
	require.NoError(t, err)
	require.NotNil(t, fields)
	require.Equal(t, hexutil.Uint(1), fields["status"])
	logs, ok := fields["logs"].([]*avmtypes.Log)
	require.True(t, ok)
	require.Len(t, logs, 1)

	// Unknown hash: nil/nil.
	fields, err = txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(types.Hash{0xde, 0xad}))
	require.NoError(t, err)
	require.Nil(t, fields)
}

func TestTransactionAPIGetTransactionByHashAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	rpcTx, err := txAPI.GetTransactionByHash(context.Background(), avmtypes.FromastHash(fx.CreateTx.Hash()))
	require.NoError(t, err)
	require.NotNil(t, rpcTx)
	require.Equal(t, avmtypes.FromastHash(fx.CreateTx.Hash()), rpcTx.Hash)

	// Unknown hash and not in the pool: nil/nil.
	rpcTx, err = txAPI.GetTransactionByHash(context.Background(), avmtypes.FromastHash(types.Hash{0xde, 0xad}))
	require.NoError(t, err)
	require.Nil(t, rpcTx)
}

func TestTransactionAPIGetTransactionByBlockHashAndIndex(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	blk, err := fx.Chain.GetBlockByNumber(apiXUint256(fx.ContractBlockNumber))
	require.NoError(t, err)

	rpcTx := txAPI.GetTransactionByBlockHashAndIndex(context.Background(), avmtypes.FromastHash(blk.Hash()), 0)
	require.NotNil(t, rpcTx)
	require.Equal(t, avmtypes.FromastHash(fx.CreateTx.Hash()), rpcTx.Hash)

	// Unknown block hash.
	require.Nil(t, txAPI.GetTransactionByBlockHashAndIndex(context.Background(), avmtypes.FromastHash(types.Hash{0xde, 0xad}), 0))
}

func TestTransactionAPIGetBlockTransactionCountByHash(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	blk, err := fx.Chain.GetBlockByNumber(apiXUint256(fx.ContractBlockNumber))
	require.NoError(t, err)

	count := txAPI.GetBlockTransactionCountByHash(context.Background(), avmtypes.FromastHash(blk.Hash()))
	require.NotNil(t, count)
	require.Equal(t, uint(2), uint(*count))

	require.Nil(t, txAPI.GetBlockTransactionCountByHash(context.Background(), avmtypes.FromastHash(types.Hash{0xde, 0xad})))
}

func TestTransactionAPISendRawTransaction(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(fx.Config.ChainID),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       21000,
		To:        &fx.Senders[1],
		Value:     uint256.NewInt(1),
	}
	txn := transaction.NewTx(inner)
	signer := transaction.NewLondonSigner(fx.Config.ChainID)
	signed, err := transaction.SignTx(txn, signer, fx.SenderKeys[0])
	require.NoError(t, err)

	raw, err := transaction.EncodeEthereumTransaction(signed)
	require.NoError(t, err)

	hash, err := txAPI.SendRawTransaction(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, avmtypes.FromastHash(signed.Hash()), hash)

	// Empty input is rejected up front.
	_, err = txAPI.SendRawTransaction(context.Background(), nil)
	require.Error(t, err)
}

