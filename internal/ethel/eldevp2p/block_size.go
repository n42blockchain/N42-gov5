// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package eldevp2p

import (
	"fmt"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// wireBlockRLPSize measures a devp2p block as geth's Block.Size does. The body
// items arrive as their in-block encodings (typed transactions already
// wrapped as RLP strings), so their lengths sum directly.
func wireBlockRLPSize(hdr *block.Header, body devp2p.BlockBody) (uint64, error) {
	var txs, uncles, withdrawals uint64
	for _, raw := range body.Transactions {
		txs += uint64(len(raw))
	}
	for _, raw := range body.Uncles {
		uncles += uint64(len(raw))
	}
	for _, raw := range body.Withdrawals {
		withdrawals += uint64(len(raw))
	}
	return ethel.RLPBlockSize(hdr, txs, uncles, withdrawals)
}

// checkWireBlockSize enforces EIP-7934 on a devp2p block from Osaka, as geth's
// ValidateBody does. The transactions are already checked against the header's
// root, but uncles and withdrawals are not, so an oversized body whose uncles
// or withdrawals do not match the header is reported as refetch: the peer
// padded it and the block itself is not shown to be invalid.
func checkWireBlockSize(cfg *params.ChainConfig, hdr *block.Header, body devp2p.BlockBody) (refetch bool, err error) {
	if cfg == nil || hdr == nil || !cfg.IsOsaka(hdr.Time) {
		return false, nil
	}
	size, err := wireBlockRLPSize(hdr, body)
	if err != nil {
		return true, err
	}
	if err := vm2.ValidateRLPBlockSize(size, true); err == nil {
		return false, nil
	}
	if !wireBodyMatchesHeader(hdr, body) {
		return true, fmt.Errorf("oversized body (%d bytes) does not match header uncles/withdrawals", size)
	}
	return false, fmt.Errorf("%w: %d bytes (max %d)", vm2.ErrRLPBlockSizeTooLarge, size, vm2.MaxRLPBlockSizeFusaka)
}

// wireBodyMatchesHeader reports whether the body's uncles and withdrawals hash
// to the header's commitments.
func wireBodyMatchesHeader(hdr *block.Header, body devp2p.BlockBody) bool {
	uncles, err := rlp.EncodeToBytes(body.Uncles)
	if err != nil || crypto.Keccak256Hash(uncles) != hdr.UncleHash {
		return false
	}
	if hdr.WithdrawalsHash == nil {
		return len(body.Withdrawals) == 0
	}
	items := make([][]byte, len(body.Withdrawals))
	for i, raw := range body.Withdrawals {
		items[i] = raw
	}
	return hash.DeriveShaErigon(rawTxList(items)) == *hdr.WithdrawalsHash
}
