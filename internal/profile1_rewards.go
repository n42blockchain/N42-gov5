// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Standard profile 1, B6: withdrawalsRoot is the rewards commitment
// keccak256(concat(rlp([address, amount]))) (keccak256("") when empty), and
// both the rewards this node computed for the block and the reward list in
// the block body must match it.

package internal

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/params"
)

// ErrProfile1Rewards: withdrawalsRoot does not commit to the block's rewards
// (B6).
var ErrProfile1Rewards = errors.New("profile 1: withdrawalsRoot does not match the rewards")

// checkProfile1Rewards applies B6 on a profile-1 chain: computed are the
// rewards Finalize paid while executing the block, body the reward list the
// block carries. Other chains are not checked.
func checkProfile1Rewards(cfg *params.ChainConfig, header *block.Header, computed, body []*block.Reward) error {
	if !cfg.IsProfile1() {
		return nil
	}
	if header.WithdrawalsHash == nil {
		return fmt.Errorf("%w: withdrawalsRoot absent at block %s", ErrProfile1Rewards, header.Number)
	}
	want := *header.WithdrawalsHash
	if got := hash.DeriveSha(block.Rewards(computed)); got != want {
		return fmt.Errorf("%w: block %s header %s, executed rewards %s", ErrProfile1Rewards, header.Number, want.Hex(), got.Hex())
	}
	if got := hash.DeriveSha(block.Rewards(body)); got != want {
		return fmt.Errorf("%w: block %s header %s, body rewards %s", ErrProfile1Rewards, header.Number, want.Hex(), got.Hex())
	}
	return nil
}
