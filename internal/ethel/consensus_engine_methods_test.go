// Copyright 2022-2026 The N42 Authors

// consensus_engine_methods_test.go — EthReplayEngine implements
// consensus.Engine purely as a replay shim: most methods are no-ops because
// imported Geth ancient data is trusted. This pins each wrapper's trivial
// contract (zero value / pass-through) so a future accidental behavior
// change (e.g. VerifyHeader starting to reject something) is caught.

package ethel

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func newTestReplayHeader(t *testing.T) *block.Header {
	t.Helper()
	h := &block.Header{
		Number:     uint256.NewInt(100),
		Difficulty: uint256.NewInt(0),
	}
	h.Coinbase[19] = 0x42
	return h
}

func TestEthReplayEngine_TrivialMethods(t *testing.T) {
	cfg := &params.ChainConfig{}
	e := NewEthReplayEngine(cfg)
	if e == nil {
		t.Fatal("NewEthReplayEngine returned nil")
	}

	h := newTestReplayHeader(t)

	author, err := e.Author(h)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	if author != h.Coinbase {
		t.Fatalf("Author = %x, want coinbase %x", author, h.Coinbase)
	}

	if e.IsServiceTransaction(types.Address{}, nil) {
		t.Fatal("IsServiceTransaction must always be false for eth replay")
	}

	if got := e.Type(); got != params.EtHashConsensus {
		t.Fatalf("Type() = %v, want EtHashConsensus", got)
	}

	if err := e.VerifyHeader(nil, h, true); err != nil {
		t.Fatalf("VerifyHeader must be a no-op, got %v", err)
	}

	abort, results := e.VerifyHeaders(nil, []block.IHeader{h, h, h}, []bool{true, false, true})
	if abort == nil || results == nil {
		t.Fatal("VerifyHeaders returned nil channels")
	}
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Fatalf("VerifyHeaders result[%d] = %v, want nil", i, err)
		}
	}

	if err := e.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("VerifyUncles must be a no-op, got %v", err)
	}

	if err := e.Prepare(nil, h); err != nil {
		t.Fatalf("Prepare must be a no-op, got %v", err)
	}

	blk, rewards, balances, err := e.FinalizeAndAssemble(nil, h, nil, nil, nil, nil)
	if blk != nil || rewards != nil || balances != nil || err != nil {
		t.Fatalf("FinalizeAndAssemble should return all zero values, got %v %v %v %v", blk, rewards, balances, err)
	}

	if err := e.Seal(nil, nil, nil, nil); err != nil {
		t.Fatalf("Seal must be a no-op, got %v", err)
	}

	if got := e.SealHash(h); got != h.Hash() {
		t.Fatalf("SealHash = %x, want header hash %x", got, h.Hash())
	}

	if got := e.CalcDifficulty(nil, 0, h); got == nil || !got.IsZero() {
		t.Fatalf("CalcDifficulty = %v, want zero", got)
	}

	if apis := e.APIs(nil); apis != nil {
		t.Fatalf("APIs should be nil, got %v", apis)
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close must be a no-op, got %v", err)
	}
}

// TestEthReplayEngine_Finalize_PostMergeSkipsReward confirms that once the
// terminal total difficulty is configured and the header carries PoS's
// zero difficulty, Finalize must not mint any block/uncle reward: rewards
// come from withdrawals processing elsewhere post-merge.
func TestEthReplayEngine_Finalize_PostMergeSkipsReward(t *testing.T) {
	cfg := &params.ChainConfig{TerminalTotalDifficulty: big.NewInt(58750000000000000)}
	e := NewEthReplayEngine(cfg)
	h := newTestReplayHeader(t)
	h.Difficulty = uint256.NewInt(0)

	rewards, balances, err := e.Finalize(nil, h, nil, nil, nil)
	if rewards != nil || balances != nil || err != nil {
		t.Fatalf("post-merge Finalize must be a no-op, got %v %v %v", rewards, balances, err)
	}
}

func TestEthReplayEngine_ImplementsConsensusEngine(t *testing.T) {
	// Compile-time assertion already exists in consensus.go; this test just
	// confirms it's reachable from a test binary without a nil-pointer panic.
	e := NewEthReplayEngine(&params.ChainConfig{})
	_ = e.Type()
}
