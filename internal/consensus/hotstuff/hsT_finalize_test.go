// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// hsTFinalizeChainConfig returns a chain config with EthELCompat enabled, so
// Finalize skips the reward function entirely (no apos dependency needed for
// this test) and goes straight to the state-root build/verify split.
func hsTFinalizeChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		HotStuff: &params.HotStuffConfig{EthELCompat: true},
	}
}

func hsTNewIBS(t *testing.T) *state.IntraBlockState {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	return state.New(state.NewPlainState(txDb, 1))
}

// TestHotStuffFinalize_BuildPathSetsRoot covers the build path (header.Root
// starts zero): Finalize must compute and set it from the IntraBlockState.
func TestHotStuffFinalize_BuildPathSetsRoot(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	rewards, unpaid, err := h.Finalize(chain, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("ethELCompat path should produce no native rewards, got %v %v", rewards, unpaid)
	}
	if header.Root == (types.Hash{}) {
		t.Fatal("expected Finalize to set header.Root on the build path")
	}
}

// TestHotStuffFinalize_VerifyPathMismatchRejected covers the verify path: a
// header whose proposer-set Root disagrees with the locally computed root
// must be rejected.
func TestHotStuffFinalize_VerifyPathMismatchRejected(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Root:       types.Hash{0xde, 0xad},
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	_, _, err := h.Finalize(chain, header, ibs, nil, nil)
	if err == nil {
		t.Fatal("expected a state root mismatch error")
	}
}

// TestHotStuffFinalize_InvalidHeaderType covers the type-assertion guard.
func TestHotStuffFinalize_InvalidHeaderType(t *testing.T) {
	h := New(nil, hsTFinalizeChainConfig())
	chain := newMockChainReader()
	if _, _, err := h.Finalize(chain, nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil/invalid header")
	}
}

// TestHotStuffFinalizeAndAssemble_AssemblesBlock covers the happy path,
// including the Shanghai withdrawals-hash repurposing and the Cancun/Prague
// optional-field defaulting performed before block assembly.
func TestHotStuffFinalizeAndAssemble_AssemblesBlock(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	b, rewards, unpaid, err := h.FinalizeAndAssemble(chain, header, ibs, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	if b == nil {
		t.Fatal("expected an assembled block")
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("unexpected native rewards under ethELCompat: %v %v", rewards, unpaid)
	}
	if b.Header().(*block.Header).Root == (types.Hash{}) {
		t.Fatal("expected the assembled header to carry the computed root")
	}
}

// TestHotStuffFinalizeAndAssemble_InvalidHeaderType covers the type-assertion
// guard in FinalizeAndAssemble, independent of the one in Finalize.
func TestHotStuffFinalizeAndAssemble_InvalidHeaderType(t *testing.T) {
	h := New(nil, hsTFinalizeChainConfig())
	chain := newMockChainReader()
	if _, _, _, err := h.FinalizeAndAssemble(chain, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil/invalid header")
	}
}

// TestHotStuffFinalize_RewardFuncPath covers the native (non-ethELCompat)
// reward path: SetRewardFunc is invoked and its rewards/balance changes are
// returned.
func TestHotStuffFinalize_RewardFuncPath(t *testing.T) {
	cfg := &params.ChainConfig{HotStuff: &params.HotStuffConfig{}}
	h := New(nil, cfg)

	rewardAddr := types.Address{0x42}
	rewardAmount := uint256.NewInt(100)

	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0)}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	h.SetRewardFunc(func(_ *params.ChainConfig, ibsArg *state.IntraBlockState, _ *block.Header, _ consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
		ibsArg.AddBalance(rewardAddr, rewardAmount)
		return []*block.Reward{{Address: rewardAddr, Amount: rewardAmount}}, nil, nil
	})

	rewards, _, err := h.Finalize(chain, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(rewards) != 1 || rewards[0].Address != rewardAddr {
		t.Fatalf("expected the reward function's reward to be returned, got %v", rewards)
	}
	if got := ibs.GetBalance(rewardAddr); got.Cmp(rewardAmount) != 0 {
		t.Fatalf("reward balance = %s, want %s", got, rewardAmount)
	}
}

// TestHotStuffFinalize_DevBlockRewardCreditsCoinbaseAndFaucet covers the
// dev-chain fixed block reward path, including the DevFaucetAddress credit.
func TestHotStuffFinalize_DevBlockRewardCreditsCoinbaseAndFaucet(t *testing.T) {
	faucet := types.Address{0x77}
	cfg := &params.ChainConfig{HotStuff: &params.HotStuffConfig{
		DevBlockReward:   50,
		DevFaucetAddress: &faucet,
	}}
	h := New(nil, cfg)
	// DevBlockReward only applies on the native (non-ethELCompat) path, which
	// requires a reward function to be set; a no-op one isolates the dev
	// reward under test.
	h.SetRewardFunc(func(_ *params.ChainConfig, _ *state.IntraBlockState, _ *block.Header, _ consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
		return nil, nil, nil
	})

	coinbase := types.Address{0x88}
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), Coinbase: coinbase}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	rewards, _, err := h.Finalize(chain, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(rewards) != 2 {
		t.Fatalf("expected coinbase + faucet dev rewards, got %v", rewards)
	}
	want := uint256.NewInt(50)
	if got := ibs.GetBalance(coinbase); got.Cmp(want) != 0 {
		t.Fatalf("coinbase balance = %s, want %s", got, want)
	}
	if got := ibs.GetBalance(faucet); got.Cmp(want) != 0 {
		t.Fatalf("faucet balance = %s, want %s", got, want)
	}
}

// TestHotStuffFinalizeAndAssemble_ForkFieldDefaulting covers the Shanghai
// withdrawals-hash repurposing and the Cancun/Prague optional-field
// defaulting performed just before block assembly.
func TestHotStuffFinalizeAndAssemble_ForkFieldDefaulting(t *testing.T) {
	cfg := &params.ChainConfig{
		HotStuff:     &params.HotStuffConfig{EthELCompat: true},
		ShanghaiTime: big.NewInt(0),
		CancunTime:   big.NewInt(0),
		PragueTime:   big.NewInt(0),
	}
	h := New(nil, cfg)

	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), Time: 0}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	b, _, _, err := h.FinalizeAndAssemble(chain, header, ibs, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	got := b.Header().(*block.Header)
	if got.WithdrawalsHash == nil {
		t.Fatal("expected WithdrawalsHash (rewards commitment) to be set under Shanghai")
	}
	if got.BlobGasUsed == nil || got.ExcessBlobGas == nil {
		t.Fatal("expected BlobGasUsed/ExcessBlobGas to default to zero under Cancun")
	}
	if got.RequestsHash == nil {
		t.Fatal("expected RequestsHash to default under Prague")
	}
}

