// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package apos

import (
	"math/big"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func newTestReward(t *testing.T) *Reward {
	t.Helper()
	return NewReward(&params.ChainConfig{
		BeijingBlock: big.NewInt(100),
		Apos: &params.APosConfig{
			RewardEpoch: 10,
			RewardLimit: big.NewInt(1000),
		},
	})
}

func TestRewardNumber2EpochBeforeBeijing(t *testing.T) {
	r := newTestReward(t)
	if got := r.number2epoch(uint256.NewInt(50)); got.Uint64() != 0 {
		t.Fatalf("expected epoch 0 before BeijingBlock, got %d", got.Uint64())
	}
}

func TestRewardNumber2EpochAfterBeijing(t *testing.T) {
	r := newTestReward(t)
	// (120-100)/10 = 2
	if got := r.number2epoch(uint256.NewInt(120)); got.Uint64() != 2 {
		t.Fatalf("expected epoch 2, got %d", got.Uint64())
	}
}

func TestRewardEpoch2Number(t *testing.T) {
	r := newTestReward(t)
	// 2*10+100 = 120
	if got := r.epoch2number(uint256.NewInt(2)); got.Uint64() != 120 {
		t.Fatalf("expected number 120, got %d", got.Uint64())
	}
}

func TestRewardNumberEpochRoundTrip(t *testing.T) {
	r := newTestReward(t)
	n := uint256.NewInt(150)
	epoch := r.number2epoch(n)
	back := r.epoch2number(epoch)
	if back.Uint64() != 150 {
		t.Fatalf("expected round-trip to land on epoch boundary 150, got %d", back.Uint64())
	}
}

func TestIsWrongStateRootBlockNumber(t *testing.T) {
	if !isWrongStateRootBlockNumber(uint256.NewInt(1288400)) {
		t.Errorf("expected 1288400 to be a known-wrong state root block")
	}
	if isWrongStateRootBlockNumber(uint256.NewInt(123)) {
		t.Errorf("expected 123 to not be a known-wrong state root block")
	}
}

func TestRewardResponseValuesSort(t *testing.T) {
	vals := RewardResponseValues{
		{Timestamp: 3},
		{Timestamp: 1},
		{Timestamp: 2},
	}
	sort.Sort(vals)
	if vals[0].Timestamp != 1 || vals[1].Timestamp != 2 || vals[2].Timestamp != 3 {
		t.Fatalf("expected ascending timestamp order, got %+v", vals)
	}
}

func TestAccountRewardsSort(t *testing.T) {
	rewards := AccountRewards{
		{Account: types.HexToAddress("0x01")},
		{Account: types.HexToAddress("0x02")},
	}
	sort.Sort(rewards)
	// Less compares in descending string order (uses > not <), so index 0 should
	// hold the lexicographically larger address after sorting.
	if rewards[0].Account.String() < rewards[1].Account.String() {
		t.Fatalf("expected descending account order, got %+v", rewards)
	}
}

func TestGetRewardsRejectsInvertedRange(t *testing.T) {
	r := newTestReward(t)
	_, err := r.GetRewards(types.Address{}, uint256.NewInt(10), uint256.NewInt(5), nil)
	if err == nil {
		t.Fatalf("expected error when from > to")
	}
}

func TestGetRewardsAggregatesMatchingAddress(t *testing.T) {
	r := newTestReward(t)
	addr := types.HexToAddress("0x01")

	getBlock := func(n *uint256.Int) (block.IBlock, error) {
		header := &block.Header{Number: n, Time: n.Uint64(), Difficulty: uint256.NewInt(1), Extra: []byte{}}
		rewards := []*block.Reward{
			{Address: addr, Amount: uint256.NewInt(7)},
			{Address: types.HexToAddress("0x02"), Amount: uint256.NewInt(99)},
		}
		return block.NewBlockFromReceipt(header, nil, nil, nil, rewards), nil
	}

	resp, err := r.GetRewards(addr, uint256.NewInt(100), uint256.NewInt(120), getBlock)
	if err != nil {
		t.Fatalf("GetRewards: %v", err)
	}
	if resp.Total.Uint64() == 0 {
		t.Fatalf("expected non-zero total reward, got %v", resp.Total)
	}
	for _, v := range resp.Data {
		if v.Value.Uint64() != 7 {
			t.Errorf("expected only addr's reward of 7, got %v", v.Value.Uint64())
		}
	}
}

func TestGetRewardsSkipsNilBlocksAndBodies(t *testing.T) {
	r := newTestReward(t)
	addr := types.HexToAddress("0x01")
	getBlock := func(n *uint256.Int) (block.IBlock, error) {
		return nil, nil
	}
	resp, err := r.GetRewards(addr, uint256.NewInt(100), uint256.NewInt(120), getBlock)
	if err != nil {
		t.Fatalf("GetRewards: %v", err)
	}
	if len(resp.Data) != 0 || resp.Total.Uint64() != 0 {
		t.Fatalf("expected empty response for nil blocks, got %+v", resp)
	}
}
