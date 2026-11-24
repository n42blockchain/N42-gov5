// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/params"
)

const p1GasLimit = uint64(480_000_000)

func profile1Config() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:    big.NewInt(4242),
		N42Profile: params.N42Profile1,
		HotStuff: &params.HotStuffConfig{
			GasLimit:        p1GasLimit,
			MaxBlockBytes:   64 << 20,
			BlockIntervalMs: 8000,
		},
	}
}

// A23: on Profile 1 the stamped gas limit is the genesis value exactly, from
// any parent and whatever --miner.gasceil says; outside the profile the
// legacy 1/1024 step toward the ceiling is unchanged.
func TestHeaderGasLimitProfile1IsGenesisConstant(t *testing.T) {
	cfg := profile1Config()
	for _, parent := range []uint64{30_000_000, p1GasLimit, 2 * p1GasLimit} {
		for _, ceil := range []uint64{0, 30_000_000, 1 << 40} {
			if got := headerGasLimit(cfg, parent, ceil); got != p1GasLimit {
				t.Fatalf("parent %d ceil %d: gas limit %d, want %d", parent, ceil, got, p1GasLimit)
			}
		}
	}
	legacy := &params.ChainConfig{ChainID: big.NewInt(1)}
	if got, want := headerGasLimit(legacy, 30_000_000, 60_000_000), CalcGasLimit(30_000_000, 60_000_000); got != want {
		t.Fatalf("legacy gas limit %d, want %d", got, want)
	}
}

// A23: a Profile-1 builder fills to the header gas limit (no fill knob).
func TestFillGasBudgetProfile1FillsToHeaderLimit(t *testing.T) {
	prev := fillGas
	t.Cleanup(func() { fillGas = prev })
	fillGas = 1_000_000
	if got := fillGasBudgetFor(profile1Config(), p1GasLimit); got != p1GasLimit {
		t.Fatalf("profile-1 fill budget %d, want %d", got, p1GasLimit)
	}
	legacy := &params.ChainConfig{ChainID: big.NewInt(1)}
	if got := fillGasBudgetFor(legacy, p1GasLimit); got != 1_000_000 {
		t.Fatalf("legacy fill budget %d, want the knob 1000000", got)
	}
}

// A23 through prepareWork: the header of a Profile-1 build carries the
// genesis gas limit although the parent and the miner ceiling differ.
func TestPrepareWorkProfile1StampsGenesisGasLimit(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	cfg := *f.Config
	cfg.N42Profile = params.N42Profile1
	cfg.HotStuff = profile1Config().HotStuff
	w.chainConfig = &cfg

	env, err := w.prepareWork(&generateParams{timestamp: uint64(time.Now().Unix()), coinbase: f.Coinbase})
	if err != nil {
		t.Fatalf("prepareWork: %v", err)
	}
	if env.header.GasLimit != p1GasLimit {
		t.Fatalf("header gas limit %d, want %d", env.header.GasLimit, p1GasLimit)
	}
	if env.gasPool.Gas() != p1GasLimit {
		t.Fatalf("gas pool %d, want %d", env.gasPool.Gas(), p1GasLimit)
	}
}

// F3/B15: the Profile-1 byte budget is maxBlockBytes/10*9 - 32 KiB - header,
// independent of the process gossip cap.
func TestBlockByteLimitProfile1FromGenesis(t *testing.T) {
	cfg := profile1Config()
	if got := blockByteLimit(cfg); got != 64<<20 {
		t.Fatalf("profile-1 byte limit %d, want 64 MiB", got)
	}
	legacy := &params.ChainConfig{ChainID: big.NewInt(1)}
	if got := blockByteLimit(legacy); got != encoder.MaxWireMessageSize() {
		t.Fatalf("legacy byte limit %d, want the wire bound %d", got, encoder.MaxWireMessageSize())
	}
	header := &block.Header{Number: nil}
	lim := newBlockSizeLimiterFor(header, blockByteLimit(cfg))
	want := int((64<<20)/blockSizeSafetyDen*blockSizeSafetyNum) - blockNonTxReserve - encodedHeaderSize(header)
	if lim.budget != want {
		t.Fatalf("budget %d, want %d", lim.budget, want)
	}
	if lim.budget <= 1<<20 {
		t.Fatalf("budget %d still bounded by the 1 MiB legacy gossip cap", lim.budget)
	}
}

// A17/F1: a Profile-1 leader seals no earlier than blockIntervalMs after it
// first held the parent.
func TestPaceProfile1WaitsIntervalAfterParentHeld(t *testing.T) {
	cfg := profile1Config()
	cfg.HotStuff.BlockIntervalMs = 120
	w := &worker{chainConfig: cfg, ctx: context.Background()}
	w.minerConf.BlockIntervalMs = cfg.HotStuff.BlockIntervalMs
	w.running = 1
	parent := types.Hash{0x01}

	held := time.Now()
	w.parentHeld.note(parent, held)
	if err := w.pace(7, parent); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(held); el < 120*time.Millisecond {
		t.Fatalf("sealed %v after the parent was held, want >= 120ms", el)
	}

	// A parent held long ago does not wait again.
	old := types.Hash{0x02}
	w.parentHeld.note(old, time.Now().Add(-time.Second))
	start := time.Now()
	if err := w.pace(8, old); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 60*time.Millisecond {
		t.Fatalf("waited %v for a parent held 1 s ago", el)
	}

	// The earliest held time wins: a later trigger does not move it.
	w.parentHeld.note(old, time.Now())
	start = time.Now()
	if err := w.pace(8, old); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 60*time.Millisecond {
		t.Fatalf("a later note moved the held time (waited %v)", el)
	}
}

// The parent-held table stays bounded.
func TestParentHeldBounded(t *testing.T) {
	var p parentHeld
	base := time.Now()
	for i := 0; i < 3*maxHeldParents; i++ {
		var h types.Hash
		h[0], h[1] = byte(i), byte(i>>8)
		p.note(h, base.Add(time.Duration(i)*time.Millisecond))
	}
	if len(p.at) > maxHeldParents {
		t.Fatalf("table holds %d entries, cap %d", len(p.at), maxHeldParents)
	}
}

// Outside the profile pace is the legacy grid: the first paced block sets
// the anchor and does not wait.
func TestPaceLegacyUsesGrid(t *testing.T) {
	w := &worker{chainConfig: &params.ChainConfig{ChainID: big.NewInt(1)}, ctx: context.Background()}
	w.minerConf.BlockIntervalMs = 5000
	w.running = 1
	start := time.Now()
	if err := w.pace(10, types.Hash{0x03}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond || !w.pacingAnchorSet {
		t.Fatal("legacy pacing did not take the grid path")
	}
	if len(w.parentHeld.at) != 0 {
		t.Fatal("legacy pacing recorded parent-held times")
	}
}
