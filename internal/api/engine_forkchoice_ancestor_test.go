package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// A forkchoice update to a canonical block below the head follows
// execution-apis #786 (the head moves back to a parent once ePBS lets a
// payload be withheld): at or below the finalized block it is skipped and
// starts no build, 32 or more blocks back it is refused with -38006, and
// otherwise the head moves back -- also to a block the overlay no longer
// holds, which the public methods then report as latest.
func TestEngineForkchoiceUpdatedToCanonicalAncestor(t *testing.T) {
	db, genesis, genesisBlock := newHiveEngineGenesisMDBXDB(t)
	defer db.Close()

	backend := &API{
		bc: &canonicalCheckChainStub{
			header: genesisBlock.Header().(*block.Header),
			blk:    genesisBlock,
			config: genesis.Config,
			db:     db,
		},
		db:            db,
		engine:        &apiTestEngine{},
		chainConfig:   genesis.Config,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(backend))
	adapter := NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{})
	engine.SetStateAdapter(adapter)
	ctx := context.Background()

	const n = 36
	hashes := []types.Hash{adapter.CurrentHeadHash()}
	for i := 1; i <= n; i++ {
		resp, err := engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: hashes[i-1]}, &PayloadAttributesV1{
			Timestamp:             hexutil.Uint64(genesisBlock.Time() + uint64(i)*12),
			PrevRandao:            types.Hash{byte(i)},
			SuggestedFeeRecipient: types.Address{0xfe},
		})
		require.NoError(t, err, "block %d", i)
		require.NotNil(t, resp.PayloadID, "block %d", i)
		payload, err := engine.GetPayloadV1(ctx, *resp.PayloadID)
		require.NoError(t, err)
		status, err := engine.NewPayloadV1(ctx, payload)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, status.Status, "block %d", i)
		hashes = append(hashes, payload.BlockHash)
	}
	finalized := hashes[2]
	resp, err := engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: hashes[n], SafeBlockHash: finalized, FinalizedBlockHash: finalized}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.Equal(t, hashes[n], adapter.CurrentHeadHash())

	attrs := &PayloadAttributesV1{Timestamp: hexutil.Uint64(genesisBlock.Time() + 10_000), SuggestedFeeRecipient: types.Address{0xfe}}

	// At or below the finalized block the node holds: VALID, no head
	// change, no build.
	for _, h := range []types.Hash{hashes[1], finalized} {
		resp, err = engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: h}, attrs)
		require.NoError(t, err)
		require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
		require.Equal(t, h, *resp.PayloadStatus.LatestValidHash)
		require.Nil(t, resp.PayloadID)
		require.Equal(t, hashes[n], adapter.CurrentHeadHash())
	}

	// 32 blocks back: too deep.
	_, err = engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: hashes[n-32], FinalizedBlockHash: finalized}, nil)
	requireEngineErrorCode(t, err, -38006)
	require.Equal(t, hashes[n], adapter.CurrentHeadHash())

	// 31 blocks back: the head moves back.
	resp, err = engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: hashes[n-31], FinalizedBlockHash: finalized}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.Equal(t, hashes[n-31], adapter.CurrentHeadHash())

	// Forward again to the old tip, which the overlay still holds.
	resp, err = engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: hashes[n], FinalizedBlockHash: finalized}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.Equal(t, hashes[n], adapter.CurrentHeadHash())

	// An ancestor the overlay does not hold (as after a restart) moves the
	// head back too, and becomes the latest block of the public methods.
	backend.engineOverlay = newEngineOverlay()
	target := hashes[n-3]
	resp, err = engine.ForkchoiceUpdatedV1(ctx, &ForkchoiceStateV1{HeadBlockHash: target, FinalizedBlockHash: finalized}, nil)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, resp.PayloadStatus.Status)
	require.Equal(t, target, adapter.CurrentHeadHash())
	latest, err := NewBlockChainAPI(backend).GetBlockByNumber(ctx, jsonrpc.LatestBlockNumber, false)
	require.NoError(t, err)
	require.Equal(t, avmtypes.FromastHash(target), latest["hash"])
}
