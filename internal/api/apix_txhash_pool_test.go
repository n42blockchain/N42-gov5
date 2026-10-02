package api

// apix_txhash_pool_test.go exercises api_transaction.go's
// GetTransactionByHash pending-pool fallback branch (a transaction not yet
// in a block but sitting in the pool) against the real executed-chain
// fixture's chain head for header resolution.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestTransactionAPIGetTransactionByHashFromPool(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	// A freshly signed transaction that was never inserted into the chain:
	// the DB lookup finds nothing, so GetTransactionByHash must fall
	// through to the pool.
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(fx.Config.ChainID),
		Nonce:     500,
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

	pending := map[types.Address][]*transaction.Transaction{
		fx.Senders[0]: {signed},
	}
	api.txspool = &mockEngineTxPool{pending: pending}
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	rpcTx, err := txAPI.GetTransactionByHash(context.Background(), avmtypes.FromastHash(signed.Hash()))
	require.NoError(t, err)
	require.NotNil(t, rpcTx)
	require.Equal(t, avmtypes.FromastHash(signed.Hash()), rpcTx.Hash)
}
