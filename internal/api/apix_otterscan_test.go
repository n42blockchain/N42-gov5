package api

// apix_otterscan_test.go exercises otterscan_api.go against the real
// executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestOtterscanGetApiLevelAndApis(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	require.Equal(t, uint64(OtterscanAPILevel), ots.GetApiLevel())

	apis := OtterscanApis(api)
	require.Len(t, apis, 1)
	require.Equal(t, "ots", apis[0].Namespace)
}

func TestOtterscanHasCode(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	has, err := ots.HasCode(context.Background(), fx.ContractAddr, latest)
	require.NoError(t, err)
	require.True(t, has)

	has, err = ots.HasCode(context.Background(), fx.Senders[0], latest)
	require.NoError(t, err)
	require.False(t, has)
}

func TestOtterscanGetBlockDetails(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	details, err := ots.GetBlockDetails(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, details)
	require.Equal(t, 2, details.Block["txCount"])
}

func TestOtterscanGetBlockTransactions(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	res, err := ots.GetBlockTransactions(context.Background(), fx.ContractBlockNumber, 0, 10)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 2, res.FullBlock["transactionCount"])
	require.Len(t, res.Receipts, 2)

	// Page past the end: empty receipts, transactionCount still reported.
	res, err = ots.GetBlockTransactions(context.Background(), fx.ContractBlockNumber, 5, 10)
	require.NoError(t, err)
	require.Empty(t, res.Receipts)
	require.Equal(t, 2, res.FullBlock["transactionCount"])
}

func TestOtterscanGetContractCreator(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	// The contract address only appears in LogAddressIndex at the call
	// block (where its LOG0 fires), not the creation block: the creation
	// block never references the address in a log. GetContractCreator scans
	// that first-seen block for a to==nil transaction, which the call block
	// does not have, so it reports no creator rather than CreateTx.
	res, err := ots.GetContractCreator(context.Background(), fx.ContractAddr)
	require.NoError(t, err)
	require.Nil(t, res)

	// An address that never appears in any log has no blocks at all.
	res, err = ots.GetContractCreator(context.Background(), types.Address{0x99})
	require.NoError(t, err)
	require.Nil(t, res)
}

func TestOtterscanGetTransactionBySenderAndNonce(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	// Sender 0's nonce-0 transaction never appears in LogAddressIndex (plain
	// transfer, no logs), so BlocksForAddress returns no candidate blocks
	// and the scan finds nothing even though the account-nonce precheck
	// passes (nonce 0 < current nonce 3 for sender 0).
	hash, err := ots.GetTransactionBySenderAndNonce(context.Background(), fx.Senders[0], 0)
	require.NoError(t, err)
	require.Nil(t, hash)

	// A future nonce is rejected up front by the account-nonce check.
	hash, err = ots.GetTransactionBySenderAndNonce(context.Background(), fx.Senders[0], 999)
	require.NoError(t, err)
	require.Nil(t, hash)
}

func TestOtterscanSearchTransactionsBeforeAfter(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	// The contract address only appears in LogAddressIndex at the block
	// that calls it (ContractBlockNumber+1, where the LOG0 fires), not the
	// creation block itself. Searching strictly before that block comes
	// back empty; querying up to and including it surfaces exactly one hit.
	logBlock := fx.ContractBlockNumber + 1

	before, err := ots.SearchTransactionsBefore(context.Background(), fx.ContractAddr, logBlock, 0)
	require.NoError(t, err)
	require.NotNil(t, before)
	require.True(t, before.FirstPage)
	require.True(t, before.LastPage)
	require.Empty(t, before.Txs)

	before, err = ots.SearchTransactionsBefore(context.Background(), fx.ContractAddr, logBlock+1, 0)
	require.NoError(t, err)
	require.True(t, before.FirstPage)
	require.True(t, before.LastPage)
	require.Len(t, before.Txs, 1)

	after, err := ots.SearchTransactionsAfter(context.Background(), fx.ContractAddr, logBlock-1, 0)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.True(t, after.FirstPage)
	require.True(t, after.LastPage)
	require.Len(t, after.Txs, 1)

	// An address with no log activity at all after this point comes back
	// empty, still well-formed.
	after, err = ots.SearchTransactionsAfter(context.Background(), fx.ContractAddr, logBlock, 0)
	require.NoError(t, err)
	require.Empty(t, after.Txs)

	// Oversized page size clamps to the default (25) rather than erroring.
	before, err = ots.SearchTransactionsBefore(context.Background(), fx.ContractAddr, logBlock+1, 1000)
	require.NoError(t, err)
	require.Len(t, before.Txs, 1)
}

func TestOtterscanGetTransactionError(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	ots := NewOtterscanAPI(api)

	// The fixture's call transaction succeeds (status 1), so there is no
	// revert data to report.
	data, err := ots.GetTransactionError(context.Background(), fx.CallTx.Hash())
	require.NoError(t, err)
	require.Nil(t, data)

	// Unknown transaction hash: no lookup entry, nil/nil.
	data, err = ots.GetTransactionError(context.Background(), types.Hash{0xde, 0xad})
	require.NoError(t, err)
	require.Nil(t, data)
}
