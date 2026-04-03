package ethel

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/params"
)

func TestEthBlockReward(t *testing.T) {
	cfg := params.EthereumMainnetChainConfig
	tests := []struct {
		block    uint64
		wantWei  string
	}{
		{0, "5000000000000000000"},           // Frontier: 5 ETH
		{4369999, "5000000000000000000"},      // Last pre-Byzantium
		{4370000, "3000000000000000000"},      // Byzantium: 3 ETH
		{7280000, "2000000000000000000"},      // Constantinople: 2 ETH
	}
	for _, tt := range tests {
		reward := ethBlockReward(cfg, tt.block)
		want, _ := uint256.FromDecimal(tt.wantWei)
		if reward.Cmp(want) != 0 {
			t.Errorf("block %d: reward=%s want=%s", tt.block, reward, want)
		}
	}
}

func TestEthUncleReward(t *testing.T) {
	blockReward := new(uint256.Int).SetUint64(5e18)

	// Uncle at depth 1: (99+8-100)/8 * 5ETH = 7/8 * 5ETH
	r := ethUncleReward(blockReward, 100, 99)
	want := new(uint256.Int).Mul(uint256.NewInt(7), blockReward)
	want.Div(want, uint256.NewInt(8))
	if r.Cmp(want) != 0 {
		t.Errorf("depth-1 uncle: got %s want %s", r, want)
	}

	// Uncle at depth 6 (max): (94+8-100)/8 * 5ETH = 2/8 * 5ETH
	r2 := ethUncleReward(blockReward, 100, 94)
	want2 := new(uint256.Int).Mul(uint256.NewInt(2), blockReward)
	want2.Div(want2, uint256.NewInt(8))
	if r2.Cmp(want2) != 0 {
		t.Errorf("depth-6 uncle: got %s want %s", r2, want2)
	}
}
