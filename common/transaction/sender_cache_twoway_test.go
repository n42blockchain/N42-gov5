// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// Two keys of one set both stay cached; a third evicts the older of the two.
func TestSenderCacheTwoWayKeepsTheNewest(t *testing.T) {
	if senderCache == nil || senderCacheMask < 1 {
		t.Skip("sender cache disabled")
	}
	signer := NewLondonSigner(big.NewInt(94))
	mk := func(home uint64, tag byte) types.Hash {
		var h types.Hash
		binary.LittleEndian.PutUint64(h[0:8], home)
		h[31] = tag
		return h
	}
	home := uint64(0x1234) &^ 1 & senderCacheMask
	a, b, c := mk(home, 1), mk(home, 2), mk(home|1, 3) // all in the set {home, home^1}
	fa, fb, fc := types.Address{1}, types.Address{2}, types.Address{3}
	senderCachePut(a, signer, fa)
	senderCachePut(b, signer, fb)
	if got, ok := senderCacheGet(a, signer); !ok || got != fa {
		t.Fatalf("a lost after a second key in its set")
	}
	if got, ok := senderCacheGet(b, signer); !ok || got != fb {
		t.Fatalf("b not cached in the alternate slot")
	}
	senderCachePut(c, signer, fc)
	if _, ok := senderCacheGet(a, signer); ok {
		t.Fatalf("the oldest key should have been evicted")
	}
	if got, ok := senderCacheGet(b, signer); !ok || got != fb {
		t.Fatalf("b evicted instead of the older a")
	}
	if got, ok := senderCacheGet(c, signer); !ok || got != fc {
		t.Fatalf("c not cached")
	}
	// Re-putting a cached key overwrites in place.
	senderCachePut(b, signer, fa)
	if got, _ := senderCacheGet(b, signer); got != fa {
		t.Fatalf("b not updated in place")
	}
	if occ, same, _ := SenderCacheProbe(c); !occ || !same {
		t.Fatalf("probe does not see c in its set")
	}
}
