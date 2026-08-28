package commitment

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/common/length"
)

// The cached hashers must be byte-identical to the uncached ones for every
// input, including cache hits, misses and single-byte address differences.
func TestAddrHashCache_MatchesUncached(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	var c addrHashCache

	addrs := make([][]byte, 4)
	for i := range addrs {
		addrs[i] = make([]byte, length.Addr)
		rnd.Read(addrs[i])
	}
	// address differing from addrs[0] in the last byte only
	near := append([]byte{}, addrs[0]...)
	near[length.Addr-1] ^= 1
	addrs = append(addrs, near)

	for i := 0; i < 2000; i++ {
		addr := addrs[rnd.Intn(len(addrs))]
		var key []byte
		switch rnd.Intn(3) {
		case 0: // account
			key = addr
		case 1: // storage
			key = make([]byte, length.Addr+length.Hash)
			copy(key, addr)
			rnd.Read(key[length.Addr:])
		default: // odd length, falls back to the uncached path
			key = make([]byte, 1+rnd.Intn(19))
			rnd.Read(key)
		}
		require.Equal(t, KeyToHexNibbleHash(key), keyToHexNibbleHashCached(key, &c), "key %x", key)
	}
}

func TestHashAddrNibbles_MatchesHashKey(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	keccak := sha3.NewLegacyKeccak256().(keccakState)
	var c addrHashCache
	var hashBuf [length.Hash]byte

	addrs := make([][]byte, 3)
	for i := range addrs {
		addrs[i] = make([]byte, length.Addr)
		rnd.Read(addrs[i])
	}
	for i := 0; i < 1000; i++ {
		addr := addrs[rnd.Intn(len(addrs))]
		depth := int16(rnd.Intn(65))
		var want, got [128]byte
		require.NoError(t, hashKey(keccak, addr, want[:], depth, hashBuf[:]))
		require.NoError(t, hashAddrNibbles(keccak, &c, addr, got[:], depth, hashBuf[:]))
		require.Equal(t, want, got, "addr %x depth %d", addr, depth)
	}
}

func TestUpdates_AddrCacheDetection(t *testing.T) {
	require.True(t, hasherReusesAddrPrefix(KeyToHexNibbleHash))
	require.True(t, hasherReusesAddrPrefix(nil))
	require.False(t, hasherReusesAddrPrefix(func(k []byte) []byte { return KeyToHexNibbleHash(k) }))
	require.False(t, hasherReusesAddrPrefix(KeyToNibblizedHash))
}
