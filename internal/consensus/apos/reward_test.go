package apos

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/params"
)

func TestNewRewardUsesMaxLimitWhenMissing(t *testing.T) {
	reward := newReward(&params.ChainConfig{
		Apos: &params.APosConfig{
			RewardEpoch: 1,
		},
	})

	if reward.rewardLimit == nil {
		t.Fatal("rewardLimit is nil")
	}
	if reward.rewardLimit.Cmp(new(uint256.Int).SetAllOne()) != 0 {
		t.Fatalf("rewardLimit = %s, want max uint256", reward.rewardLimit)
	}
}

func TestNewRewardUsesMaxLimitOnOverflow(t *testing.T) {
	overflowLimit := new(big.Int).Lsh(big.NewInt(1), 300)
	reward := newReward(&params.ChainConfig{
		Apos: &params.APosConfig{
			RewardEpoch: 1,
			RewardLimit: overflowLimit,
		},
	})

	if reward.rewardLimit == nil {
		t.Fatal("rewardLimit is nil")
	}
	if reward.rewardLimit.Cmp(new(uint256.Int).SetAllOne()) != 0 {
		t.Fatalf("rewardLimit = %s, want max uint256", reward.rewardLimit)
	}
}
