// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// followerTestChain is the reward reader Finalize asserts on. The APoS reward
// module pays nothing on a chain without an Apos section, so its methods are
// never reached.
type followerTestChain struct {
	consensus.ChainHeaderReader
}

func (followerTestChain) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) { return nil, nil }
func (followerTestChain) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}

func followerTestChainConfig() *params.ChainConfig {
	faucet := types.HexToAddress("0x42e9819036f61bF665D5f727E8C03121f12f586e")
	return &params.ChainConfig{
		ChainID:    big.NewInt(1144),
		Consensus:  params.HotStuffConsensus,
		N42Profile: params.N42Profile1,
		HotStuff: &params.HotStuffConfig{
			DevBlockReward:   params.NewDevReward(1_000_000_000_000_000_000),
			DevFaucetAddress: &faucet,
		},
	}
}

// executeFollowerTestBlock runs the finalisation of an empty block 1 on a fresh
// state with engine and returns the rewards and the executed state root.
func executeFollowerTestBlock(t *testing.T, engine consensus.Engine) ([]*block.Reward, types.Hash) {
	t.Helper()
	tx := memdb.BeginRw(t, memdb.NewTestDB(t))
	ibs := state.New(state.NewPlainState(tx, 1))
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Coinbase:   types.HexToAddress("0x1111111111111111111111111111111111111111"),
		Time:       1,
	}
	rewards, _, err := engine.Finalize(followerTestChain{}, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("finalize block 1: %v", err)
	}
	root, ok := ibs.LastIntermediateRoot()
	if !ok {
		t.Fatal("finalize computed no executed root")
	}
	return rewards, root
}

// A node that does not mine (no BLS key, no --engine.miner) must execute a
// profile-1 block with the B6 rewards exactly as a validator does: the reward
// function is the engine's from construction, not from the mining path.
func TestHotStuffFollowerExecutesBlockRewards(t *testing.T) {
	cfg := followerTestChainConfig()
	follower, err := resolveN42ConsensusEngine(&conf.Config{ChainCfg: cfg}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The validator's engine as the mining path wired it before the reward
	// function moved to construction.
	validator := hotstuff.New(cfg.HotStuff, cfg)
	validator.SetRewardFunc(func(chainCfg *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, chain consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
		return apos.DoReward(chainCfg, ibs, header, chain)
	})

	wantRewards, wantRoot := executeFollowerTestBlock(t, validator)
	gotRewards, gotRoot := executeFollowerTestBlock(t, follower)
	if gotRoot != wantRoot {
		t.Fatalf("follower executed root %x, validator %x", gotRoot, wantRoot)
	}
	if len(gotRewards) != 2 || len(wantRewards) != 2 {
		t.Fatalf("rewards: follower %d, validator %d, want coinbase and faucet", len(gotRewards), len(wantRewards))
	}
	for i := range wantRewards {
		if gotRewards[i].Address != wantRewards[i].Address || !gotRewards[i].Amount.Eq(wantRewards[i].Amount) {
			t.Fatalf("reward %d: follower %+v, validator %+v", i, gotRewards[i], wantRewards[i])
		}
	}
}
