// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

type floorGateFetcher struct {
	behind  uint64
	catchUp chan struct{}
}

func (f *floorGateFetcher) FetchBlockByHash(types.Hash) {}
func (f *floorGateFetcher) CatchUp() {
	select {
	case f.catchUp <- struct{}{}:
	default:
	}
}
func (f *floorGateFetcher) HeightBehind() uint64 { return f.behind }

// TestUnlockedLeaderBehindPeersCatchesUpInsteadOfProposing is the leader
// half of S131: node 0, one block short of f343 and with no lock after the
// reset, built 88e236 on its own committed head. With no lock and peers
// ahead it must catch up and leave the view to a validator at the top.
func TestUnlockedLeaderBehindPeersCatchesUpInsteadOfProposing(t *testing.T) {
	fetcher := &floorGateFetcher{behind: 1, catchUp: make(chan struct{}, 1)}
	producer := &executionTestProducer{}
	svc := &Service{ctx: context.Background(), blockFetcher: fetcher, blockProducer: producer}

	svc.triggerBlockProduction(2, types.Hash{})
	if len(producer.parents) != 0 {
		t.Fatalf("unlocked leader behind peers produced: %v", producer.parents)
	}
	<-fetcher.catchUp

	fetcher.behind = 0
	svc.triggerBlockProduction(3, types.Hash{})
	if len(producer.parents) != 1 || producer.parents[0] != (types.Hash{}) {
		t.Fatalf("unlocked leader at the top height did not produce: %v", producer.parents)
	}
}

// TestLeaderRefusesParentConflictingWithCommitted: a lock naming a block
// off this node's committed chain must not be built on (the leader votes
// for its own proposal).
func TestLeaderRefusesParentConflictingWithCommitted(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetCommittedChain(s131Chain())
	e.importedParents[s131Sibling] = s131Parent
	producer := &executionTestProducer{}
	svc := &Service{ctx: context.Background(), blockFetcher: &floorGateFetcher{}, blockProducer: producer,
		engine: &HotStuff{engine: e}}

	svc.triggerBlockProduction(2, s131Sibling)
	if len(producer.parents) != 0 {
		t.Fatalf("produced on a parent that conflicts with the committed block: %v", producer.parents)
	}
	svc.triggerBlockProduction(3, s131Committed)
	if len(producer.parents) != 1 || producer.parents[0] != s131Committed {
		t.Fatalf("did not produce on the committed block: %v", producer.parents)
	}
}
