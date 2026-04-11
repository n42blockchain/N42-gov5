// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// state_root_verify.go — Ethereum-standard state root verification
// using the minimal MPT implementation (no CalcTrieRoot dependency).

package ethel

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethcompat"
	"github.com/n42blockchain/N42/lib/kv"
)

// VerifyStateRoot computes the Ethereum state root by rebuilding an MPT from
// plain state. The implementation lives in internal/ethcompat so callers
// outside this package can reuse it without importing internal/ethel.
func VerifyStateRoot(tx kv.RwTx) (types.Hash, error) {
	return ethcompat.VerifyStateRoot(tx)
}
