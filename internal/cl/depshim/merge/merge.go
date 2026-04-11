// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Merge unit for the merge package.
// Declares the BlockNonce type aliases.
// Merge-fork helper shims.

//go:build n42el

// Package merge provides the post-merge constant header fields that the
// cl/ pure-type layer stamps onto headers when converting CL execution
// payloads back into RLP-style EL headers.
package merge

import "math/big"

// ProofOfStakeDifficulty is the difficulty value an EL header must carry
// after the merge. The yellow-paper says zero.
var ProofOfStakeDifficulty = big.NewInt(0)

// BlockNonce mirrors the [8]byte nonce slot in an EL header. We define it
// locally so we do not have to import a heavyweight execution/types package
// just for one type.
type BlockNonce [8]byte

// ProofOfStakeNonce is the all-zero nonce required by the post-merge spec.
var ProofOfStakeNonce = BlockNonce{}
