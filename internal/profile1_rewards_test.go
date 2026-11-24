// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestCheckProfile1Rewards(t *testing.T) {
	p1 := &params.ChainConfig{N42Profile: params.N42Profile1}
	rewards := []*block.Reward{
		{Address: types.Address{1}, Amount: uint256.NewInt(1e18)},
		{Address: types.Address{2}, Amount: uint256.NewInt(1e18)},
	}
	root := hash.DeriveSha(block.Rewards(rewards))
	h := &block.Header{Number: uint256.NewInt(1), WithdrawalsHash: &root}
	if err := checkProfile1Rewards(p1, h, rewards, rewards); err != nil {
		t.Fatalf("matching rewards refused: %v", err)
	}
	for name, tc := range map[string]struct {
		h              *block.Header
		computed, body []*block.Reward
	}{
		"executed rewards differ":         {h, rewards[:1], rewards},
		"body rewards differ":             {h, rewards, rewards[1:]},
		"withdrawalsRoot absent":          {&block.Header{Number: uint256.NewInt(1)}, rewards, rewards},
		"empty list vs keccak empty root": {h, nil, nil},
	} {
		if err := checkProfile1Rewards(p1, tc.h, tc.computed, tc.body); !errors.Is(err, ErrProfile1Rewards) {
			t.Errorf("%s: %v, want %v", name, err, ErrProfile1Rewards)
		}
	}
	// No rewards commit to keccak256("") (B6).
	empty := hash.NilHash
	if err := checkProfile1Rewards(p1, &block.Header{Number: uint256.NewInt(1), WithdrawalsHash: &empty}, nil, nil); err != nil {
		t.Fatalf("empty rewards against keccak(\"\"): %v", err)
	}
	// Legacy chains are not checked.
	if err := checkProfile1Rewards(&params.ChainConfig{}, &block.Header{Number: uint256.NewInt(1)}, rewards, nil); err != nil {
		t.Fatalf("legacy chain checked: %v", err)
	}
}
