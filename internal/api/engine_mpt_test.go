// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// rawList is a DerivableList of pre-encoded values.
type rawList [][]byte

func (l rawList) Len() int                            { return len(l) }
func (l rawList) EncodeIndex(i int, w *bytes.Buffer)  { w.Write(l[i]) }

// TestDeriveEthereumListHash spot-checks that the engine API's
// deriveEthereumListHash returns valid (non-empty for non-empty input,
// canonical EmptyRootHash for empty) trie roots over a few representative
// shapes. Cross-implementation correctness is asserted in
// common/hash/derive_sha_erigon_test.go and end-to-end against real
// mainnet headers in internal/ethel/witness_7m_test.go.
func TestDeriveEthereumListHash(t *testing.T) {
	if got := deriveEthereumListHash(rawList(nil)); got != hash.EmptyRootHash {
		t.Errorf("empty: got %s, want %s", got.Hex(), hash.EmptyRootHash.Hex())
	}

	entries := make(rawList, 38)
	for i := range entries {
		entries[i] = bytes.Repeat([]byte{byte(i + 1)}, 64+i)
	}
	if got := deriveEthereumListHash(entries); got == (types.Hash{}) {
		t.Error("38-entry list returned zero hash")
	}
}
