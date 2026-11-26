// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"testing"
)

// TestLatestSignerForChainIDShared: repeated calls return one shared signer
// (no per-transaction allocation retained by sender-cache entries), and the
// shared signer does not alias the caller's chain-ID value.
func TestLatestSignerForChainIDShared(t *testing.T) {
	id := big.NewInt(1144)
	a := LatestSignerForChainID(id).(londonSigner)
	b := LatestSignerForChainID(big.NewInt(1144)).(londonSigner)
	if a.chainId != b.chainId {
		t.Fatal("LatestSignerForChainID built a new signer for the same chain")
	}
	id.SetInt64(7)
	if a.ChainID().Int64() != 1144 {
		t.Fatalf("cached signer aliases the caller's big.Int: chain id now %v", a.ChainID())
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = LatestSignerForChainID(b.chainId) }); allocs > 0 {
		t.Fatalf("LatestSignerForChainID allocates %.0f objects per call", allocs)
	}
	if c := LatestSignerForChainID(big.NewInt(5)); c.ChainID().Int64() != 5 {
		t.Fatalf("other chain: got chain id %v", c.ChainID())
	}
}
