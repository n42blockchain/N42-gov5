package api

// engineAdapterWireT_test.go covers the ExecutePayloadFromWireWithFullVerification
// fixture-import path and the Reorg wrapper (cache purge + ReorgWithSource/Reorg
// dispatch), reusing the hive genesis-DB + first-empty-payload fixtures already
// built in engine_state_adapter_hive_test.go.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestExecutePayloadFromWireWithFullVerificationAcceptsFirstEmptyPayload(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)
	payload := hiveFirstEmptyPayload()
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)

	valid, stateRoot, err := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).
		ExecutePayloadFromWireWithFullVerification(blk.(*block.Block), nil)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, payload.StateRoot, stateRoot)
}

func TestExecutePayloadFromWireWithFullVerificationRejectsBadStateRoot(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)
	payload := hiveFirstEmptyPayload()
	payload.StateRoot = types.HexToHash("0xbad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0bad0")
	blk, err := executionPayloadV1ToBlock(payload)
	require.NoError(t, err)

	valid, _, err := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}).
		ExecutePayloadFromWireWithFullVerification(blk.(*block.Block), nil)
	require.NoError(t, err)
	require.False(t, valid)
}

func TestEngineStateAdapterReorgNoopWhenAtOrBeforeTarget(t *testing.T) {
	db, genesis, _ := newHiveEngineGenesisDB(t)
	a := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{})

	// Fresh chaindata: ethel.ReadProgress(tx) is 0, so Reorg(0) hits the
	// currentHead <= targetBlock early-return without touching the freezer,
	// but still exercises the hashedReadCache.PurgeAll() + nil-csSource
	// dispatch to ethel.Reorg at the top of the method.
	require.NoError(t, a.Reorg(0))
}
