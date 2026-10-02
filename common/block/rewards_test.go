package block

import (
	"bytes"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/stretchr/testify/require"
)

func TestRewardsSortInterface(t *testing.T) {
	r := require.New(t)
	rewards := Rewards{
		{Address: types.HexToAddress("0x01"), Amount: uint256.NewInt(1)},
		{Address: types.HexToAddress("0xFF"), Amount: uint256.NewInt(2)},
		{Address: types.HexToAddress("0x80"), Amount: uint256.NewInt(3)},
	}
	r.Equal(3, rewards.Len())

	sort.Sort(rewards)
	// Less is descending (Compare > 0), so sorted order is highest address first.
	r.True(rewards[0].Address.String() > rewards[1].Address.String())
	r.True(rewards[1].Address.String() > rewards[2].Address.String())
}

func TestRewardsSwap(t *testing.T) {
	r := require.New(t)
	rewards := Rewards{
		{Address: types.HexToAddress("0x01")},
		{Address: types.HexToAddress("0x02")},
	}
	first, second := rewards[0], rewards[1]
	rewards.Swap(0, 1)
	r.Equal(second, rewards[0])
	r.Equal(first, rewards[1])
}

func TestRewardsEncodeIndex(t *testing.T) {
	r := require.New(t)
	rewards := Rewards{
		{Address: types.HexToAddress("0x01"), Amount: uint256.NewInt(5)},
	}
	var buf bytes.Buffer
	rewards.EncodeIndex(0, &buf)
	r.NotEmpty(buf.Bytes())
}

func TestRewardProtoRoundTrip(t *testing.T) {
	r := require.New(t)
	original := &Reward{Address: types.HexToAddress("0xAB"), Amount: uint256.NewInt(42)}
	pb := original.ToProtoMessage()
	pbReward, ok := pb.(*types_pb.Reward)
	r.True(ok)

	var decoded Reward
	got := decoded.FromProtoMessage(pbReward)
	r.Same(&decoded, got)
	r.Equal(original.Address, decoded.Address)
	r.Equal(original.Amount, decoded.Amount)
}
