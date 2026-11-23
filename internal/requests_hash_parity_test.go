package internal

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func requestsParityConfig(parity *big.Int) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:            big.NewInt(1),
		Consensus:          params.Faker,
		LondonBlock:        big.NewInt(0),
		PectraTime:         big.NewInt(0),
		EthereumParityTime: parity,
	}
}

// Under the parity time a built header carries the EIP-7685 hash of the
// block's requests -- sha256 of nothing when there are none, not the empty
// trie root -- and an imported header is held to it.
func TestRequestsHashUnderEthereumParity(t *testing.T) {
	cfg := requestsParityConfig(big.NewInt(100))
	emptyRequests := types.Hash(sha256.Sum256(nil))
	requests := []hexutil.Bytes{{0x01, 0xaa, 0xbb}}

	header := &block.Header{Number: uint256.NewInt(1), Time: 100}
	StampRequestsHash(cfg, header, nil)
	require.NotNil(t, header.RequestsHash)
	require.Equal(t, emptyRequests, *header.RequestsHash)
	require.NoError(t, VerifyRequestsHash(cfg, header, nil))
	require.Error(t, VerifyRequestsHash(cfg, header, requests), "requests the header does not commit to")

	StampRequestsHash(cfg, header, requests)
	require.Equal(t, ExecutionRequestsHash(requests), *header.RequestsHash)
	require.NoError(t, VerifyRequestsHash(cfg, header, requests))

	emptyRoot := hash.EmptyRootHash
	require.Error(t, VerifyRequestsHash(cfg, &block.Header{Number: uint256.NewInt(1), Time: 100, RequestsHash: &emptyRoot}, nil), "the empty trie root is not the EIP-7685 value")
	require.Error(t, VerifyRequestsHash(cfg, &block.Header{Number: uint256.NewInt(1), Time: 100}, nil), "a missing hash")
}

// Before the parity time, and on a chain without one, headers are neither
// stamped nor checked.
func TestRequestsHashWithoutEthereumParity(t *testing.T) {
	for _, cfg := range []*params.ChainConfig{requestsParityConfig(big.NewInt(100)), requestsParityConfig(nil)} {
		header := &block.Header{Number: uint256.NewInt(1), Time: 99}
		StampRequestsHash(cfg, header, []hexutil.Bytes{{0x01, 0xaa}})
		require.Nil(t, header.RequestsHash)
		require.NoError(t, VerifyRequestsHash(cfg, header, []hexutil.Bytes{{0x01, 0xaa}}))
	}
}
