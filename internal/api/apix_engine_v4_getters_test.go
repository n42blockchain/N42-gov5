package api

// apix_engine_v4_getters_test.go exercises engine_api_v4.go's simple
// capability/schedule getters and GetBlobsV1's null-entries paths, plus the
// pure computeVersionedHash helper and engine_api_v1.go's
// syncingPayloadResponse.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

func TestEngineAPIv4GetBlobScheduleV1(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIv4(bcAPI)

	resp, err := eng.GetBlobScheduleV1(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotZero(t, resp.BlobGasPerBlob)
}

func TestEngineAPIv4GetClientCapabilitiesV1(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIv4(bcAPI)

	caps, err := eng.GetClientCapabilitiesV1(context.Background())
	require.NoError(t, err)
	require.NotNil(t, caps)
	require.NotEmpty(t, caps.SupportedMethods)
	require.True(t, caps.CanCancelFork)
}

func TestEngineAPIv4GetForkCandidatesV1(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIv4(bcAPI)

	status, err := eng.GetForkCandidatesV1(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, "cancun", status.ActiveFork)
	require.Len(t, status.Candidates, 1)
}

func TestEngineAPIv4GetBlobsV1(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)
	eng := NewEngineAPIv4(bcAPI)

	// No hashes requested.
	res, err := eng.GetBlobsV1(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, res)

	// Too many hashes requested.
	tooMany := make([]types.Hash, 129)
	_, err = eng.GetBlobsV1(context.Background(), tooMany)
	require.Error(t, err)

	// No engine overlay wired on this fixture's API: every requested hash
	// comes back as a null entry (correct length, all nil).
	res, err = eng.GetBlobsV1(context.Background(), []types.Hash{{0x01}, {0x02}})
	require.NoError(t, err)
	require.Len(t, res, 2)
	require.Nil(t, res[0])
	require.Nil(t, res[1])
}

func TestComputeVersionedHash(t *testing.T) {
	commitment := make([]byte, 48)
	h := computeVersionedHash(commitment)
	require.Equal(t, byte(0x01), h[0])
}

func TestSyncingPayloadResponse(t *testing.T) {
	resp := syncingPayloadResponse()
	require.Equal(t, PayloadStatusSyncing, resp.Status)
	require.Nil(t, resp.LatestValidHash)

	fcResp := syncingForkchoiceResponse()
	require.Equal(t, PayloadStatusSyncing, fcResp.PayloadStatus.Status)
}
