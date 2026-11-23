// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

type namingFetcher struct {
	fakeBlockFetcher
	nmu   sync.Mutex
	named []types.Hash
}

func (f *namingFetcher) NoteConsensusNamed(h types.Hash) {
	f.nmu.Lock()
	defer f.nmu.Unlock()
	f.named = append(f.named, h)
}

// A verified proposal's block is reported to the sync layer as consensus
// named (branch-switch authority for the chain's lag executor).
func TestExecuteBlockNamesTheProposedBlock(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	fetcher := &namingFetcher{}
	s := &Service{engine: h, blockFetcher: fetcher, pendingExecutions: make(map[types.Hash]struct{})}
	hash := types.Hash{0x42}
	s.handleOutput(EngineOutput{Type: OutputExecuteBlock, Hash: hash}, time.Time{})
	fetcher.nmu.Lock()
	defer fetcher.nmu.Unlock()
	if len(fetcher.named) != 1 || fetcher.named[0] != hash {
		t.Fatalf("named = %x, want [%x]", fetcher.named, hash)
	}
}
