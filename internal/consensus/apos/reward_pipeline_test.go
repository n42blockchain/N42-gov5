// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY with even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Covers AccumulateRewards and DoReward against a fake N42ChainHeaderReader
// that supplies deposit info, prior unpaid balances, and per-block verifier
// lists -- the inputs these two 0%-covered functions actually read.

package apos

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// aposTSetVerifiers attaches a verifier list to a block built by
// aposTChain.add/GetBlockByNumber, type-asserting the IBody interface back
// to the concrete *block.Body so the exported Verifiers field can be set
// (there is no constructor that takes verifiers directly).
func aposTSetVerifiers(t *testing.T, blk block.IBlock, verifiers []*block.Verify) {
	t.Helper()
	body, ok := blk.Body().(*block.Body)
	if !ok {
		t.Fatalf("expected *block.Body, got %T", blk.Body())
	}
	body.Verifiers = verifiers
}

// Note: AccumulateRewards has a latent underflow defect when endNumber==0
// (i.e. number==rewardEpoch exactly): its `currentNr.SubUint64(currentNr, 1)`
// wraps uint64(0) to math.MaxUint64, and the loop condition
// `currentNr.Cmp(endNumber) >= 0` stays true, so it tries to fetch block
// MaxUint64 and fails with "block not found" instead of stopping at 0. Not
// fixed here (non-test code); the tests below choose numbers where
// endNumber >= 1 to avoid tripping it.

func TestAccumulateRewardsPaysUnderLimit(t *testing.T) {
	chain := newAposTChain()
	addr := types.HexToAddress("0x01")

	// number=4, rewardEpoch=3 -> endNumber=1; walks blocks 3,2,1.
	for n := uint64(1); n <= 4; n++ {
		h := &block.Header{Number: uint256.NewInt(n)}
		chain.add(h)
		blk, err := chain.GetBlockByNumber(uint256.NewInt(n))
		if err != nil {
			t.Fatalf("GetBlockByNumber: %v", err)
		}
		aposTSetVerifiers(t, blk, []*block.Verify{{Address: addr}})
	}
	chain.depositLow[addr] = uint256.NewInt(10)
	chain.depositMax[addr] = uint256.NewInt(1000)

	r := NewReward(&params.ChainConfig{Apos: &params.APosConfig{RewardEpoch: 3, RewardLimit: big.NewInt(1_000_000)}})
	payMap, unpayMap, err := AccumulateRewards(r, uint256.NewInt(4), chain)
	if err != nil {
		t.Fatalf("AccumulateRewards: %v", err)
	}
	// 3 blocks of verifier activity at 10/block = 30, under the 1,000,000 limit -> unpaid.
	if got := payMap[addr]; got == nil || got.Sign() != 0 {
		t.Fatalf("expected zero pay (under limit), got %v", got)
	}
	if got := unpayMap[addr]; got == nil || got.Cmp(uint256.NewInt(30)) != 0 {
		t.Fatalf("expected 30 unpaid, got %v", got)
	}
}

func TestAccumulateRewardsPaysAtLimitWithCarriedUnpaid(t *testing.T) {
	chain := newAposTChain()
	addr := types.HexToAddress("0x02")

	// number=3, rewardEpoch=2 -> endNumber=1; walks blocks 2,1.
	for n := uint64(1); n <= 3; n++ {
		h := &block.Header{Number: uint256.NewInt(n)}
		chain.add(h)
		blk, err := chain.GetBlockByNumber(uint256.NewInt(n))
		if err != nil {
			t.Fatalf("GetBlockByNumber: %v", err)
		}
		aposTSetVerifiers(t, blk, []*block.Verify{{Address: addr}})
	}
	chain.depositLow[addr] = uint256.NewInt(40)
	chain.depositMax[addr] = uint256.NewInt(1000)
	chain.unpaid[addr] = uint256.NewInt(25) // carried over from a prior epoch

	r := NewReward(&params.ChainConfig{Apos: &params.APosConfig{RewardEpoch: 2, RewardLimit: big.NewInt(100)}})
	payMap, unpayMap, err := AccumulateRewards(r, uint256.NewInt(3), chain)
	if err != nil {
		t.Fatalf("AccumulateRewards: %v", err)
	}
	// 2 blocks * 40 = 80, + 25 carried = 105 >= 100 limit -> paid in full, unpaid reset to 0.
	if got := payMap[addr]; got == nil || got.Cmp(uint256.NewInt(105)) != 0 {
		t.Fatalf("expected 105 paid, got %v", got)
	}
	if got := unpayMap[addr]; got == nil || got.Sign() != 0 {
		t.Fatalf("expected zero unpaid once limit is met, got %v", got)
	}
}

func TestAccumulateRewardsUnderflowGuard(t *testing.T) {
	chain := newAposTChain()
	r := NewReward(&params.ChainConfig{Apos: &params.APosConfig{RewardEpoch: 100, RewardLimit: big.NewInt(1)}})
	payMap, unpayMap, err := AccumulateRewards(r, uint256.NewInt(5), chain)
	if err != nil || payMap != nil || unpayMap != nil {
		t.Fatalf("expected nil,nil,nil when number < rewardEpoch; got %v,%v,%v", payMap, unpayMap, err)
	}
}

func TestAccumulateRewardsMissingBlockErrors(t *testing.T) {
	chain := newAposTChain()
	r := NewReward(&params.ChainConfig{Apos: &params.APosConfig{RewardEpoch: 1, RewardLimit: big.NewInt(1)}})
	// Number 1 exists nowhere in chain.byNumber, so GetBlockByNumber returns (nil,nil).
	if _, _, err := AccumulateRewards(r, uint256.NewInt(1), chain); err == nil {
		t.Fatalf("expected error for missing block")
	}
}

func TestDoRewardNoConfigIsNoOp(t *testing.T) {
	chain := newAposTChain()
	header := &block.Header{Number: uint256.NewInt(10)}
	rewards, unpaid, err := DoReward(nil, nil, header, chain)
	if err != nil || rewards != nil || unpaid != nil {
		t.Fatalf("expected nil,nil,nil for nil chain config; got %v,%v,%v", rewards, unpaid, err)
	}
}

func TestDoRewardAppliesEpochRewardAndCreatesAccount(t *testing.T) {
	addr := types.HexToAddress("0x03")
	chain := newAposTChain()
	// number=4, rewardEpoch=2 -> endNumber=2; walks blocks 3,2.
	for n := uint64(1); n <= 4; n++ {
		h := &block.Header{Number: uint256.NewInt(n)}
		chain.add(h)
		blk, err := chain.GetBlockByNumber(uint256.NewInt(n))
		if err != nil {
			t.Fatalf("GetBlockByNumber: %v", err)
		}
		aposTSetVerifiers(t, blk, []*block.Verify{{Address: addr}})
	}
	chain.depositLow[addr] = uint256.NewInt(500)
	chain.depositMax[addr] = uint256.NewInt(10_000)

	chainConf := &params.ChainConfig{
		BeijingBlock: big.NewInt(0),
		Apos:         &params.APosConfig{RewardEpoch: 2, RewardLimit: big.NewInt(100)},
	}

	dbA := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, dbA)
	ibs := state.New(state.NewPlainState(tx, 4))

	header := &block.Header{Number: uint256.NewInt(4)}
	rewards, unpaid, err := DoReward(chainConf, ibs, header, chain)
	if err != nil {
		t.Fatalf("DoReward: %v", err)
	}
	if len(rewards) != 1 || rewards[0].Address != addr {
		t.Fatalf("expected one reward for %v, got %+v", addr, rewards)
	}
	if got := unpaid[addr]; got == nil || got.Sign() != 0 {
		t.Fatalf("expected unpaid reset to zero once limit met, got %v", got)
	}
	if !ibs.Exist(addr) {
		t.Fatalf("expected DoReward to create the rewarded account")
	}
}

func TestDoRewardSkipsOffEpochBoundary(t *testing.T) {
	chain := newAposTChain()
	chainConf := &params.ChainConfig{
		BeijingBlock: big.NewInt(0),
		Apos:         &params.APosConfig{RewardEpoch: 7, RewardLimit: big.NewInt(100)},
	}
	header := &block.Header{Number: uint256.NewInt(3)} // not a multiple of RewardEpoch(7)
	rewards, unpaid, err := DoReward(chainConf, nil, header, chain)
	if err != nil {
		t.Fatalf("DoReward: %v", err)
	}
	if len(rewards) != 0 || unpaid != nil {
		t.Fatalf("expected no reward off the epoch boundary, got %v, %v", rewards, unpaid)
	}
}
