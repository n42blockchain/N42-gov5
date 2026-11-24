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

// requestsHashConfig is a chain that has run Prague rules since genesis
// through PectraTime, with the given Prague time.
func requestsHashConfig(prague *big.Int) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:     big.NewInt(1),
		Consensus:   params.Faker,
		LondonBlock: big.NewInt(0),
		PectraTime:  big.NewInt(0),
		PragueTime:  prague,
	}
}

// From the Prague time a built header carries the EIP-7685 hash of the
// block's requests -- sha256 of nothing when there are none, not the empty
// trie root -- and an imported header is held to it.
func TestRequestsHashFromPragueTime(t *testing.T) {
	cfg := requestsHashConfig(big.NewInt(100))
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

// Before the Prague time, and on a chain without one, headers are neither
// stamped nor checked.
func TestRequestsHashBeforePragueTime(t *testing.T) {
	for _, cfg := range []*params.ChainConfig{requestsHashConfig(big.NewInt(100)), requestsHashConfig(nil)} {
		header := &block.Header{Number: uint256.NewInt(1), Time: 99}
		StampRequestsHash(cfg, header, []hexutil.Bytes{{0x01, 0xaa}})
		require.Nil(t, header.RequestsHash)
		require.NoError(t, VerifyRequestsHash(cfg, header, []hexutil.Bytes{{0x01, 0xaa}}))
	}
}

// On a profile-1 chain (AM25) requestsHash is a deferred field: the builder
// stamps the block's own EIP-7685 hash (and replaces it with the parent's
// before sealing), and the block's own execution does not check the header's
// value -- the next header carries it, checked against the stored result.
func TestRequestsHashProfile1IsDeferred(t *testing.T) {
	cfg := requestsHashConfig(big.NewInt(0))
	cfg.N42Profile = params.N42Profile1
	requests := []hexutil.Bytes{{0x01, 0xaa}}
	require.True(t, RequestsHashDeferred(cfg, 100))

	header := &block.Header{Number: uint256.NewInt(1), Time: 100}
	StampRequestsHash(cfg, header, requests)
	require.NotNil(t, header.RequestsHash)
	require.Equal(t, ExecutionRequestsHash(requests), *header.RequestsHash)

	sha := types.Hash(sha256.Sum256(nil))
	require.NoError(t, VerifyRequestsHash(cfg, &block.Header{Number: uint256.NewInt(1), Time: 100, RequestsHash: &sha}, requests),
		"a block that produces requests is valid and carries its parent's value")
	require.False(t, RequestsHashDeferred(requestsHashConfig(big.NewInt(0)), 100), "legacy chains keep the per-block check")
}
