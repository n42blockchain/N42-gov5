package api

// apix_overrides_test.go exercises api.go's StateOverride.Apply and
// BlockOverrides.Apply, the eth_call override plumbing, using a real
// *state.IntraBlockState over the executed-chain fixture's database (any
// writable IntraBlockState works; these overrides don't touch the chain).

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
)

func apiXFreshIntraBlockState(t *testing.T, fx *apiXChainFixture) *state.IntraBlockState {
	t.Helper()
	roTx, err := fx.DB.BeginRo(context.Background())
	require.NoError(t, err)
	t.Cleanup(roTx.Rollback)
	reader := state.NewPlainStateReader(roTx)
	return state.New(reader)
}

func TestStateOverrideApplyNilAndEmpty(t *testing.T) {
	var nilDiff *StateOverride
	require.NoError(t, nilDiff.Apply(nil))
}

func TestStateOverrideApplyNonceCodeBalance(t *testing.T) {
	fx := apiXGetChainFixture(t)
	ibs := apiXFreshIntraBlockState(t, fx)

	addr := avmtypes.FromastAddress(&fx.Senders[0])
	nonce := hexutil.Uint64(42)
	code := hexutil.Bytes{0x60, 0x00}
	balanceBig := (*hexutil.Big)(big.NewInt(99))
	diff := StateOverride{
		*addr: OverrideAccount{
			Nonce:   &nonce,
			Code:    &code,
			Balance: &balanceBig,
		},
	}
	require.NoError(t, diff.Apply(ibs))

	require.Equal(t, uint64(42), ibs.GetNonce(fx.Senders[0]))
	require.Equal(t, []byte(code), ibs.GetCode(fx.Senders[0]))
	require.Equal(t, uint256.NewInt(99), ibs.GetBalance(fx.Senders[0]))
}

func TestStateOverrideApplyStateDiffAndStatsPrintConflict(t *testing.T) {
	fx := apiXGetChainFixture(t)
	ibs := apiXFreshIntraBlockState(t, fx)

	addr := avmtypes.FromastAddress(&fx.Senders[0])
	key := avmcommon.Hash(types.Hash{0x01})
	val := avmcommon.Hash(types.Hash{0x02})
	m := map[avmcommon.Hash]avmcommon.Hash{key: val}

	// Both StatsPrint ("state") and StateDiff set: rejected.
	diff := StateOverride{
		*addr: OverrideAccount{StatsPrint: &m, StateDiff: &m},
	}
	require.Error(t, diff.Apply(ibs))

	// StateDiff alone applies cleanly.
	diff = StateOverride{
		*addr: OverrideAccount{StateDiff: &m},
	}
	require.NoError(t, diff.Apply(ibs))

	// StatsPrint ("state") alone applies cleanly too.
	diff = StateOverride{
		*addr: OverrideAccount{StatsPrint: &m},
	}
	require.NoError(t, diff.Apply(ibs))
}

func TestBlockOverridesApplyNilAndFields(t *testing.T) {
	var nilOverrides *BlockOverrides
	require.NoError(t, nilOverrides.Apply(nil))

	num := (*hexutil.Big)(big.NewInt(7))
	diff := (*hexutil.Big)(big.NewInt(5))
	tm := hexutil.Uint64(123)
	gasLimit := hexutil.Uint64(30_000_000)
	coinbase := types.Address{0x01}
	random := types.Hash{0x02}
	baseFee := (*hexutil.Big)(big.NewInt(3))

	overrides := &BlockOverrides{
		Number:     num,
		Difficulty: diff,
		Time:       &tm,
		GasLimit:   &gasLimit,
		Coinbase:   &coinbase,
		Random:     &random,
		BaseFee:    baseFee,
	}

	var ctx evmtypes.BlockContext
	require.NoError(t, overrides.Apply(&ctx))
	require.Equal(t, uint64(7), ctx.BlockNumber)
	require.Equal(t, big.NewInt(5), ctx.Difficulty)
	require.Equal(t, uint64(123), ctx.Time)
	require.Equal(t, uint64(30_000_000), ctx.GasLimit)
	require.Equal(t, coinbase, ctx.Coinbase)
	require.Equal(t, &random, ctx.PrevRanDao)
	require.NotNil(t, ctx.BaseFee)
	require.Equal(t, uint64(3), ctx.BaseFee.Uint64())
}
