// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Engine API MPT helpers for execution payload derivation.
// rawRLP passes already-encoded bytes through the rlp encoder without
// an extra length wrap, which is required by blockRLPTransaction for
// legacy transactions whose payload bytes are themselves a valid RLP
// list. deriveEthereumListHash computes the canonical Ethereum MPT
// trie root for a derivable list via hash.DeriveShaErigon.

package api

import (
	"io"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// rawRLP wraps already-RLP-encoded bytes so they pass through the rlp encoder
// without an additional length-prefix wrap. Used by blockRLPTransaction for
// legacy transactions whose payload bytes are already a valid RLP list.
type rawRLP []byte

func (r rawRLP) EncodeRLP(w io.Writer) error {
	_, err := w.Write(r)
	return err
}

// deriveEthereumListHash computes the canonical Ethereum MPT trie root of
// a derivable list (transactions, withdrawals, ...) used in execution
// payloads. Thin alias over hash.DeriveShaErigon.
func deriveEthereumListHash(list hash.DerivableList) types.Hash {
	return hash.DeriveShaErigon(list)
}
