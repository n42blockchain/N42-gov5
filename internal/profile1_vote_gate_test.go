// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// p1GateEngine is a consensus engine with a scripted vote-time header check.
type p1GateEngine struct {
	coreTFakerEngine
	err   error
	calls int
}

func (e *p1GateEngine) VoteGateHeaderCheck(consensus.ChainHeaderReader, *block.Header, time.Time) error {
	e.calls++
	return e.err
}

var errP1StaticRefused = errors.New("profile 1: scripted static refusal")

// p1GuardChain is guardChain on a profile-1 chain with engine, and the
// parent's execution result stored (profile 1 is deferred from block 0).
func p1GuardChain(t *testing.T, engine consensus.Engine, result rawdb.ExecutedResult) (*BlockChain, block.IBlock) {
	t.Helper()
	bc, parent, child := guardChain(t, true)
	bc.chainConfig = &params.ChainConfig{N42Profile: params.N42Profile1}
	bc.engine = engine
	tx, err := bc.ChainDB.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := rawdb.WriteExecutedResult(tx, parent.Hash(), result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return bc, block.NewBlock(child, nil)
}

// parentResult is the execution result guardChain's child carries.
func parentResult(blk block.IBlock) rawdb.ExecutedResult {
	h := blk.Header().(*block.Header)
	return rawdb.ExecutedResult{Root: h.Root, ReceiptHash: h.ReceiptHash, Bloom: h.Bloom, GasUsed: h.GasUsed}
}

// A block the static header rules refuse fails the profile-1 check: no
// EventBlockChecked, so no Round-1 vote (A13, AM19, AM23).
func TestProfile1DeferredCheckRefusesAStaticFailure(t *testing.T) {
	_, probe := p1GuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{})
	eng := &p1GateEngine{err: errP1StaticRefused}
	bc, blk := p1GuardChain(t, eng, parentResult(probe))

	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || !errors.Is(err, errP1StaticRefused) {
		t.Fatalf("checked=%v retry=%v err=%v, want a final static refusal", checked, retry, err)
	}
	if eng.calls != 1 {
		t.Fatalf("vote header check ran %d times, want 1", eng.calls)
	}
}

// A header whose parent is not stored yet asks for a retry, not a refusal.
func TestProfile1DeferredCheckRetriesAnUnknownParent(t *testing.T) {
	eng := &p1GateEngine{err: consensus.ErrUnknownAncestor}
	bc, blk := p1GuardChain(t, eng, rawdb.ExecutedResult{})

	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || !retry || !errors.Is(err, consensus.ErrUnknownAncestor) {
		t.Fatalf("checked=%v retry=%v err=%v, want a retry", checked, retry, err)
	}
}

// A block already executed here still passes through the vote-time check:
// its import never applied the clock bound (B4).
func TestProfile1DeferredCheckDoesNotSkipTheHeaderCheckForAnAppliedBlock(t *testing.T) {
	eng := &p1GateEngine{err: errP1StaticRefused}
	bc, blk := p1GuardChain(t, eng, rawdb.ExecutedResult{})
	tx, err := bc.ChainDB.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteQMDBApplied(tx, 11, blk.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bc.CheckDeferredBlock(blk); !errors.Is(err, errP1StaticRefused) {
		t.Fatalf("an applied block skipped the vote-time header check: %v", err)
	}
}

// An engine without the vote-time check fails closed on profile 1.
func TestProfile1DeferredCheckFailsClosedWithoutAHeaderCheck(t *testing.T) {
	bc, blk := p1GuardChain(t, coreTFakerEngine{}, rawdb.ExecutedResult{})
	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || !errors.Is(err, ErrProfile1NoHeaderCheck) {
		t.Fatalf("checked=%v retry=%v err=%v, want ErrProfile1NoHeaderCheck", checked, retry, err)
	}
}

// A header whose carried results differ from this node's execution of the
// parent is refused as a deferred-result failure (AM19).
func TestProfile1DeferredCheckRefusesWrongCarriedResults(t *testing.T) {
	_, probe := p1GuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{})
	wrong := parentResult(probe)
	wrong.GasUsed++
	bc, blk := p1GuardChain(t, &p1GateEngine{}, wrong)

	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || !errors.Is(err, ErrProfile1DeferredCheck) {
		t.Fatalf("checked=%v retry=%v err=%v, want ErrProfile1DeferredCheck", checked, retry, err)
	}
}

// With the static rules and the carried results right, the check passes.
func TestProfile1DeferredCheckPasses(t *testing.T) {
	_, probe := p1GuardChain(t, &p1GateEngine{}, rawdb.ExecutedResult{})
	eng := &p1GateEngine{}
	bc, blk := p1GuardChain(t, eng, parentResult(probe))

	checked, retry, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("checked=%v retry=%v err=%v", checked, retry, err)
	}
	if eng.calls != 1 {
		t.Fatalf("vote header check ran %d times, want 1", eng.calls)
	}
}
