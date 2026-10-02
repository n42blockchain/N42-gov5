package api

// apix_misc_test.go exercises api_misc.go's n42API gas helpers, DebugAPI's
// Db*/NodeStatus and TxsPoolAPI's Content/Status/Inspect against the real
// executed-chain fixture. These API's gpo is left nil (not wired by the
// fixture), exercising each function's documented nil-oracle fallback.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestN42APIGasPriceNoOracle(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	n42 := NewN42API(api)

	price, err := n42.GasPrice(context.Background())
	require.NoError(t, err)
	require.NotNil(t, price)

	tip, err := n42.MaxPriorityFeePerGas(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tip)
}

func TestN42APIFeeHistoryNoOracle(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	n42 := NewN42API(api)

	_, err := n42.FeeHistory(context.Background(), 1, 0, nil)
	require.Error(t, err)
}

func TestDebugAPIDbGetAndStats(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	// Unknown key in a real table: "not found".
	_, err := debug.DbGet(context.Background(), "PlainState", []byte("does-not-exist"))
	require.Error(t, err)

	stats, err := debug.DbStats(context.Background())
	require.NoError(t, err)
	require.NotNil(t, stats)

	// GetAccount is a documented no-op.
	debug.GetAccount(context.Background(), fx.Senders[0])
}

func TestDebugAPINodeStatus(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	status, err := debug.NodeStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, uint64(5), status.CurrentBlock)
	require.Equal(t, fx.Config.ChainID.String(), status.ChainID)
	require.False(t, status.Syncing)
}

func TestTxsPoolAPIContentStatusInspect(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	pending := map[types.Address][]*transaction.Transaction{
		fx.Senders[0]: {fx.CallTx},
	}
	api.txspool = &mockEngineTxPool{pending: pending}
	poolAPI := NewTxsPoolAPI(api)

	content := poolAPI.Content()
	require.Contains(t, content, "pending")
	require.Contains(t, content, "queued")
	require.NotEmpty(t, content["pending"])

	status := poolAPI.Status()
	require.Equal(t, hexutil.Uint(1), status["pending"])
	require.Equal(t, hexutil.Uint(0), status["queued"])

	inspect := poolAPI.Inspect()
	require.NotEmpty(t, inspect["pending"])
}

func TestNetAPIBasics(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	net := NewNetAPI(api, 1337)

	require.True(t, net.Listening())
	require.Equal(t, hexutil.Uint(0), net.PeerCount())
	require.Equal(t, "1337", net.Version())
}
