// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import "bytes"

// EthTransactions is a DerivableList wrapper that emits standard Ethereum
// raw transaction encoding (legacy RLP or EIP-2718 typed envelope) for each
// element. Use this with hash.DeriveShaErigon to compute the canonical
// Ethereum transaction trie root.
//
// The plain Transactions type is for N42 native blocks: its EncodeIndex
// produces protobuf bytes which feed N42's simplified hash.DeriveSha
// (keccak256 of concatenated values, not an MPT trie). The two encodings
// are intentionally different and cannot be mixed.
type EthTransactions []*Transaction

func (s EthTransactions) Len() int { return len(s) }

func (s EthTransactions) EncodeIndex(i int, w *bytes.Buffer) {
	enc, err := EncodeEthereumTransaction(s[i])
	if err != nil {
		// EncodeEthereumTransaction only fails on unsupported tx type.
		// In practice all txs that reach this path are well-formed.
		panic(err)
	}
	w.Write(enc)
}
