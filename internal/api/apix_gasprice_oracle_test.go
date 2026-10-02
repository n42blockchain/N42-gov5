package api

// apix_gasprice_oracle_test.go wires a real gasprice.Oracle to the
// executed-chain fixture's chain and exercises n42API's GasPrice,
// MaxPriorityFeePerGas and FeeHistory through it, covering
// Oracle.SuggestTipCap/getBlockValues and Oracle.FeeHistory on real blocks.

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func apiXNewRealOracle(fx *apiXChainFixture) *Oracle {
	return NewOracle(fx.Chain, nil, fx.Config, conf.GpoConfig{
		Blocks:           3,
		Percentile:       60,
		Default:          big.NewInt(1_000_000_000),
		MaxHeaderHistory: 10,
		MaxBlockHistory:  10,
		MaxPrice:         big.NewInt(500_000_000_000),
		IgnorePrice:      big.NewInt(2),
	})
}

func TestN42APIGasPriceWithRealOracle(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.gpo = apiXNewRealOracle(fx)
	n42 := NewN42API(api)

	price, err := n42.GasPrice(context.Background())
	require.NoError(t, err)
	require.NotNil(t, price)
	require.True(t, price.ToInt().Sign() > 0)

	tip, err := n42.MaxPriorityFeePerGas(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tip)

	// Second call hits the lastHead cache fast path.
	price2, err := n42.GasPrice(context.Background())
	require.NoError(t, err)
	require.Equal(t, price.ToInt(), price2.ToInt())
}

func TestN42APIFeeHistoryWithRealOracle(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.gpo = apiXNewRealOracle(fx)
	n42 := NewN42API(api)

	result, err := n42.FeeHistory(context.Background(), 3, jsonrpc.LatestBlockNumber, []float64{50})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.OldestBlock)
	require.NotEmpty(t, result.BaseFee)

	// Out-of-range percentile is rejected.
	_, err = n42.FeeHistory(context.Background(), 3, jsonrpc.LatestBlockNumber, []float64{150})
	require.Error(t, err)

	// Zero blocks requested: no data, no error.
	result, err = n42.FeeHistory(context.Background(), 0, jsonrpc.LatestBlockNumber, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
}
