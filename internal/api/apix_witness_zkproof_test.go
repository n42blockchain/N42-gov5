package api

// apix_witness_zkproof_test.go exercises WitnessAPI.GetBlockWitness (JMT
// not enabled on this QMDB-commitment fixture) and ZKProofAPI's
// GetBlockZKProof/VerifyBlockZKProof (no ZK proof embedded in any fixture
// block) against the real executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestWitnessAPIGetBlockWitnessNoJMT(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	witnessAPI := NewWitnessAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	_, err := witnessAPI.GetBlockWitness(context.Background(), latest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "JMT commitment must be enabled")

	apis := witnessAPI.APIs()
	require.Len(t, apis, 1)
	require.Equal(t, "eth", apis[0].Namespace)
}

func TestWitnessAPIGetBlockWitnessUnknownBlock(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	witnessAPI := NewWitnessAPI(api)

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999999))
	_, err := witnessAPI.GetBlockWitness(context.Background(), bnh)
	require.Error(t, err)
}

func TestZKProofAPIGetBlockZKProofUnavailable(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	zkAPI := NewZKProofAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	res, err := zkAPI.GetBlockZKProof(context.Background(), latest)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Available)

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999999))
	_, err = zkAPI.GetBlockZKProof(context.Background(), bnh)
	require.Error(t, err)
}

func TestZKProofAPIVerifyBlockZKProofUnavailable(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	zkAPI := NewZKProofAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	ok, err := zkAPI.VerifyBlockZKProof(context.Background(), latest)
	require.Error(t, err)
	require.False(t, ok)

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999999))
	_, err = zkAPI.VerifyBlockZKProof(context.Background(), bnh)
	require.Error(t, err)
}
