package api

// apix_blockscout_test.go exercises blockscout.go's
// GetTransactionByBlockNumberAndIndex and GetProof against the real
// executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestTransactionAPIGetTransactionByBlockNumberAndIndexHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	rpcTx := txAPI.GetTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), 1)
	require.NotNil(t, rpcTx)
	require.Equal(t, avmtypes.FromastHash(fx.ValueTransferTxs[len(fx.ValueTransferTxs)-1].Hash()), rpcTx.Hash)

	// Index past the end of a real block: documented F2 fallback, which
	// returns nil when no F2 ledger is wired.
	rpcTx = txAPI.GetTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), 100)
	require.Nil(t, rpcTx)
}

func TestBlockChainAPIGetProofAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	res, err := bcAPI.GetProof(context.Background(), fx.Senders[0], nil, latest)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotEmpty(t, res.AccountProof)
}

func TestBlockChainAPIAccountsEmptyWithoutManager(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	require.Empty(t, bcAPI.Accounts())
}
