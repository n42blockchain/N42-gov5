// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package empty

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// `empty` package doesn't have any dependencies by intention - to reduce future "circular deps problems"
var (
	// RootHash is the known root hash of an empty merkle trie.
	RootHash = common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	// UncleHash is the known hash of the empty uncle set.
	UncleHash = common.HexToHash("1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347") // rlpHash([]*types.Header(nil))

	// CodeHash is the known hash of the empty EVM bytecode.
	CodeHash = common.HexToHash("c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470") // crypto.Keccak256Hash(nil)

	// TxsHash is the known hash of the empty transaction set.
	TxsHash = common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	// EmptyReceiptsHash is the known hash of the empty receipt set.
	ReceiptsHash = common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	// WithdrawalsHash is the known hash of the empty withdrawal set.
	WithdrawalsHash = common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	// RequestsHash is the known hash of an empty request set, sha256("").
	RequestsHash = common.HexToHash("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	// BlockAccessListHash is the known hash of an empty block access list, keccak256(rlp.encode([])).
	BlockAccessListHash = common.HexToHash("1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")
)
