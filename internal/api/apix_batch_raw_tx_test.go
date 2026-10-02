package api

// apix_batch_raw_tx_test.go exercises api_transaction.go's
// BatchRawTransaction against real signed transactions from the
// executed-chain fixture's keys.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
)

func apiXSignedRawTx(t *testing.T, fx *apiXChainFixture, keyIdx int, nonce uint64) hexutil.Bytes {
	t.Helper()
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(fx.Config.ChainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       21000,
		To:        &fx.Senders[(keyIdx+1)%len(fx.Senders)],
		Value:     uint256.NewInt(1),
	}
	txn := transaction.NewTx(inner)
	signer := transaction.NewLondonSigner(fx.Config.ChainID)
	signed, err := transaction.SignTx(txn, signer, fx.SenderKeys[keyIdx])
	require.NoError(t, err)
	raw, err := transaction.EncodeEthereumTransaction(signed)
	require.NoError(t, err)
	return raw
}

func TestTransactionAPIBatchRawTransactionEmpty(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	hashes, err := txAPI.BatchRawTransaction(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, hashes)
}

func TestTransactionAPIBatchRawTransactionOversized(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	inputs := make([]hexutil.Bytes, MaxBatchSize+1)
	_, err := txAPI.BatchRawTransaction(context.Background(), inputs)
	require.Error(t, err)
}

func TestTransactionAPIBatchRawTransactionMixedValidInvalid(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	good1 := apiXSignedRawTx(t, fx, 0, 200)
	good2 := apiXSignedRawTx(t, fx, 1, 201)
	bad := hexutil.Bytes{} // empty entry: decode error

	hashes, err := txAPI.BatchRawTransaction(context.Background(), []hexutil.Bytes{good1, bad, good2})
	require.Error(t, err) // first error (from the bad entry) is surfaced
	require.Len(t, hashes, 3)
	require.NotEqual(t, avmcommon.Hash{}, hashes[0])
	require.Equal(t, avmcommon.Hash{}, hashes[1])
	require.NotEqual(t, avmcommon.Hash{}, hashes[2])
}
