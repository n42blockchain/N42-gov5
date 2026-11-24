// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// p1TestChain is deferredTestChain on a chain config of choice, with every
// block applied.
type p1TestChain struct {
	*deferredTestChain
	cfg *params.ChainConfig
}

func (c *p1TestChain) Config() *params.ChainConfig             { return c.cfg }
func (c *p1TestChain) HasAppliedBlock(types.Hash, uint64) bool { return true }

func newP1CheckService(cfg *params.ChainConfig, results []deferredResult, notifier *recordingNotifier) (*Service, *p1TestChain) {
	chain := &p1TestChain{deferredTestChain: &deferredTestChain{results: results}, cfg: cfg}
	return &Service{cfg: &config{chain: chain, blockImportNotifier: notifier}}, chain
}

var p1Config = &params.ChainConfig{N42Profile: params.N42Profile1}

// On profile 1 a block that arrived only by an import path is checked after
// it applies, so the engine can vote it (A13, AM23).
func TestProfile1ImportedBlockIsChecked(t *testing.T) {
	notifier := &recordingNotifier{}
	svc, chain := newP1CheckService(p1Config, []deferredResult{{checked: true}}, notifier)
	blk := testBlock(21, types.Hash{0xA1})

	svc.profile1CheckImported(blk)
	if chain.callCount() != 1 || notifier.checkedCount() != 1 {
		t.Fatalf("calls=%d checked=%d, want 1 and 1", chain.callCount(), notifier.checkedCount())
	}
	// A passed block is not verified twice.
	svc.profile1CheckImported(blk)
	if chain.callCount() != 1 {
		t.Fatalf("a passed block was checked again: %d calls", chain.callCount())
	}
}

// A block refused by the check stays refused: a later import path does not
// re-run the check (a timestamp refused by the clock bound must not pass once
// the clock has moved on).
func TestProfile1RefusedBlockStaysRefused(t *testing.T) {
	notifier := &recordingNotifier{}
	svc, chain := newP1CheckService(p1Config, []deferredResult{
		{checked: true, err: errors.New("profile 1: timestamp too far ahead of the local clock")},
		{checked: true},
	}, notifier)
	blk := testBlock(22, types.Hash{0xA2})

	svc.deferredCheck(blk)
	svc.profile1CheckImported(blk)
	if chain.callCount() != 1 || notifier.checkedCount() != 0 {
		t.Fatalf("calls=%d checked=%d, want 1 and 0", chain.callCount(), notifier.checkedCount())
	}
}

// Other chains are unchanged: no check after an import.
func TestNonProfileImportedBlockIsNotChecked(t *testing.T) {
	notifier := &recordingNotifier{}
	svc, chain := newP1CheckService(&params.ChainConfig{}, []deferredResult{{checked: true}}, notifier)
	svc.profile1CheckImported(testBlock(23, types.Hash{0xA3}))
	if chain.callCount() != 0 || notifier.checkedCount() != 0 {
		t.Fatalf("calls=%d checked=%d on a non-profile chain", chain.callCount(), notifier.checkedCount())
	}
}
