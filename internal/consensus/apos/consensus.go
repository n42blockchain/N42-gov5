package apos

import (
	"errors"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func AccumulateRewards(r *Reward, number *uint256.Int, chain consensus.ChainHeaderReader) (map[types.Address]*uint256.Int, map[types.Address]*uint256.Int, error) {
	rewardMap := make(map[types.Address]*uint256.Int)
	unpayMap := make(map[types.Address]*uint256.Int)

	// Security: prevent underflow when number < rewardEpoch
	if number.Cmp(r.rewardEpoch) < 0 {
		return nil, nil, nil
	}

	endNumber := new(uint256.Int).Sub(number, r.rewardEpoch)
	//calculate last batch but this one
	currentNr := number.Clone()
	currentNr.SubUint64(currentNr, 1)

	depositeMap := map[types.Address]struct {
		reward    *uint256.Int
		maxReward *uint256.Int
	}{}
	for currentNr.Cmp(endNumber) >= 0 {
		block, err := chain.GetBlockByNumber(currentNr)
		if err != nil {
			return nil, nil, err
		}

		verifiers := block.Body().Verifier()
		for _, verifier := range verifiers {
			_, ok := depositeMap[verifier.Address]
			if !ok {
				low, max := chain.GetDepositInfo(verifier.Address)
				if low == nil || max == nil {
					continue
				}
				depositeMap[verifier.Address] = struct {
					reward    *uint256.Int
					maxReward *uint256.Int
				}{reward: low, maxReward: max}

				log.Debug("account deposite infos", "addr", verifier.Address, "perblock", low, "perepoch", max)
			}

			addrReward, ok := rewardMap[verifier.Address]
			if !ok {
				addrReward = uint256.NewInt(0)
			}

			// Security: create new uint256.Int to avoid modifying the original pointer in map
			sum := new(uint256.Int).Add(addrReward, depositeMap[verifier.Address].reward)
			rewardMap[verifier.Address] = math.Min256(sum, depositeMap[verifier.Address].maxReward.Clone())
		}

		currentNr.SubUint64(currentNr, 1)
	}

	for addr, amount := range rewardMap {
		var payAmount, unpayAmount *uint256.Int

		lastSedi, err := chain.GetAccountRewardUnpaid(addr)
		if err != nil {
			log.Debug("build reward Big map get account reward error,err=", err)
			return nil, nil, err
		}
		if lastSedi != nil {
			amount.Add(amount, lastSedi)
		}

		if amount.Cmp(r.rewardLimit) >= 0 {
			payAmount = amount.Clone()
			unpayAmount = uint256.NewInt(0)
		} else {
			payAmount = uint256.NewInt(0)
			unpayAmount = amount.Clone()
		}

		rewardMap[addr] = payAmount
		unpayMap[addr] = unpayAmount
	}

	return rewardMap, unpayMap, nil
}

func doReward(chainConf *params.ChainConfig, state *state.IntraBlockState, header *block.Header, chain consensus.ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	beijing, overflow := uint256.FromBig(chainConf.BeijingBlock)
	if overflow {
		return nil, nil, errors.New("BeijingBlock overflows uint256")
	}
	number := header.Number64()
	var rewards block.Rewards
	var upayMap map[types.Address]*uint256.Int

	// Security: ensure number >= beijing before subtraction to prevent underflow
	if chainConf.IsBeijing(number.Uint64()) && number.Cmp(beijing) >= 0 && new(uint256.Int).Mod(new(uint256.Int).Sub(number, beijing), uint256.NewInt(chainConf.Apos.RewardEpoch)).
		Cmp(uint256.NewInt(0)) == 0 {
		r := newReward(chainConf)
		var (
			err    error
			payMap map[types.Address]*uint256.Int
		)
		payMap, upayMap, err = AccumulateRewards(r, number, chain)
		if err != nil {
			return nil, nil, err
		}
		for addr, value := range payMap {
			if value.Cmp(uint256.NewInt(0)) > 0 {
				if !state.Exist(addr) {
					state.CreateAccount(addr, false)
				}

				log.Info("🔨 set account reward", "addr", addr, "amount", value.Uint64(), "blockNr", header.Number.Uint64())
				state.AddBalance(addr, value)
				rewards = append(rewards, &block.Reward{
					Address: addr,
					Amount:  value,
				})
			}
		}
		if !isWrongStateRootBlockNumber(header.Number64()) {
			state.SoftFinalise()
		}
		sort.Sort(rewards)
	}
	return rewards, upayMap, nil
}

func isWrongStateRootBlockNumber(blockNr *uint256.Int) bool {
	switch blockNr.Uint64() {
	case 1288400, 1299200, 1310000, 1320800, 1331600, 1342400, 1353200, 1364000, 1374800, 1385600, 1396400, 1407200, 1418000, 1428800, 1439600, 1450400:
		return true
	default:
		return false
	}
}
