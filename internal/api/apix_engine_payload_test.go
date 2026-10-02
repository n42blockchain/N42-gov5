package api

// apix_engine_payload_test.go exercises the stateful execution-payload
// builders (engine_api_v1.go/engine_api_blob.go/engine_api_v4.go) against
// the real executed-chain fixture's head, plus a couple of small pure
// helpers used by the Engine API's invalid-payload paths.

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func apiXHeadBlockAndHash(fx *apiXChainFixture) (block.IBlock, types.Hash) {
	head := fx.Chain.CurrentBlock()
	return head, head.Hash()
}

// apiXCancunConfig clones the fixture's chain config with Shanghai/Cancun
// active from genesis: buildExecutionPayloadV3Stateful/V4Stateful only
// populate BlobGasUsed/ExcessBlobGas (and so return a non-nil payload) once
// Cancun is active. The fixture's own shared Config must not be mutated (it
// is read by every other test via the sync.Once fixture), so this makes an
// independent copy for the V3/V4 engine API tests only.
func apiXCancunConfig(fx *apiXChainFixture) *params.ChainConfig {
	cfg := *fx.Config
	cfg.ShanghaiBlock = big.NewInt(0)
	cfg.CancunBlock = big.NewInt(0)
	cfg.ShanghaiTime = big.NewInt(0)
	cfg.CancunTime = big.NewInt(0)
	return &cfg
}

func TestBuildExecutionPayloadV1StatefulFromHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIV1(bcAPI)

	head, headHash := apiXHeadBlockAndHash(fx)
	headHeader := blockHeader(head)
	require.NotNil(t, headHeader)

	attrs := &PayloadAttributesV1{
		Timestamp:             hexutil.Uint64(headHeader.Time + 10),
		SuggestedFeeRecipient: fx.Senders[0],
	}

	payload := eng.buildExecutionPayloadV1(head, headHash, attrs)
	require.NotNil(t, payload)
	require.Equal(t, headHash, payload.ParentHash)

	payloadWithValue, value := eng.buildExecutionPayloadV1WithValue(head, headHash, attrs)
	require.NotNil(t, payloadWithValue)
	require.NotNil(t, value)
}

func TestBuildExecutionPayloadV2StatefulFromHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIV1(bcAPI)

	head, headHash := apiXHeadBlockAndHash(fx)
	headHeader := blockHeader(head)
	require.NotNil(t, headHeader)

	// Nil attrs: documented nil/nil short circuit.
	payload, value := eng.buildExecutionPayloadV2StatefulWithValue(head, headHash, nil)
	require.Nil(t, payload)
	require.Nil(t, value)

	attrs := &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             hexutil.Uint64(headHeader.Time + 10),
			SuggestedFeeRecipient: fx.Senders[0],
		},
	}
	payload = eng.buildExecutionPayloadV2Stateful(head, headHash, attrs)
	require.NotNil(t, payload)
	require.Equal(t, headHash, payload.ParentHash)

	payload, value = eng.buildExecutionPayloadV2StatefulWithValue(head, headHash, attrs)
	require.NotNil(t, payload)
	require.NotNil(t, value)
}

func TestBuildExecutionPayloadV3StatefulFromHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	api.chainConfig = apiXCancunConfig(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIBlob(bcAPI)

	head, headHash := apiXHeadBlockAndHash(fx)
	headHeader := blockHeader(head)
	require.NotNil(t, headHeader)

	beaconRoot := types.Hash{0x01}
	attrs := &PayloadAttributesV3{
		Timestamp:             hexutil.Uint64(headHeader.Time + 10),
		SuggestedFeeRecipient: fx.Senders[0],
		ParentBeaconBlockRoot: &beaconRoot,
	}

	payload := eng.buildExecutionPayloadV3Stateful(head, headHash, attrs)
	require.NotNil(t, payload)
	require.Equal(t, headHash, payload.ParentHash)

	payload, value, bundle := eng.buildExecutionPayloadV3StatefulWithValue(head, headHash, attrs)
	require.NotNil(t, payload)
	require.NotNil(t, value)
	require.NotNil(t, bundle)
}

func TestBuildExecutionPayloadV4StatefulFromHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	api.txspool = &mockEngineTxPool{}
	api.chainConfig = apiXCancunConfig(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIv4(bcAPI)

	head, headHash := apiXHeadBlockAndHash(fx)
	headHeader := blockHeader(head)
	require.NotNil(t, headHeader)

	beaconRoot := types.Hash{0x02}
	attrs := &PayloadAttributesV4{
		Timestamp:             hexutil.Uint64(headHeader.Time + 10),
		SuggestedFeeRecipient: fx.Senders[0],
		ParentBeaconBlockRoot: &beaconRoot,
	}

	payload, requests, value, bundle := eng.buildExecutionPayloadV4Stateful(head, headHash, attrs)
	require.NotNil(t, payload)
	require.Equal(t, headHash, payload.ParentHash)
	require.NotNil(t, value)
	require.NotNil(t, bundle)
	// No EIP-7685 requests expected from plain value transfers/contract calls.
	require.Empty(t, requests)
}

func TestInvalidPayloadResponseForParent(t *testing.T) {
	fx := apiXGetChainFixture(t)
	headHeader := fx.Chain.CurrentBlock().Header().(*block.Header)

	// With a known parent header, the response carries LatestValidHash set
	// to the parent's hash.
	parentHash := headHeader.ParentHash
	resp := invalidPayloadResponseForParent("boom", parentHash, headHeader)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.NotNil(t, resp.LatestValidHash)
	require.Equal(t, parentHash, *resp.LatestValidHash)

	// With no parent header, falls back to the plain invalid response (no
	// latest-valid hash to anchor on).
	resp = invalidPayloadResponseForParent("boom", parentHash, nil)
	require.Equal(t, PayloadStatusInvalid, resp.Status)
	require.Nil(t, resp.LatestValidHash)
}

func TestCloneExecutionValidationBlock(t *testing.T) {
	fx := apiXGetChainFixture(t)
	head := fx.Chain.CurrentBlock()

	cloned, ok := cloneExecutionValidationBlock(head)
	require.True(t, ok)
	require.NotNil(t, cloned)
	require.Equal(t, head.Hash(), cloned.Hash())

	// A non-*block.Block IBlock is rejected.
	_, ok = cloneExecutionValidationBlock(nil)
	require.False(t, ok)
}
