package initialsync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
)

func TestBlocksQueueResetFromBlockNr(t *testing.T) {
	q := &blocksQueue{
		smm:           newStateMachineManager(),
		blocksFetcher: &blocksFetcher{blocksPerPeriod: 10},
	}

	// Seed with some unrelated state machines that should be wiped.
	q.smm.addStateMachine(uint256.NewInt(1))
	q.smm.addStateMachine(uint256.NewInt(2))

	start := uint256.NewInt(100)
	if err := q.resetFromBlockNr(context.Background(), start); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.smm.machines) == 0 {
		t.Fatalf("expected new state machines to be added")
	}
	if _, ok := q.smm.findStateMachine(start); !ok {
		t.Fatalf("expected a state machine starting at %v", start)
	}
	// Old machines must be gone.
	if _, ok := q.smm.findStateMachine(uint256.NewInt(1)); ok {
		t.Fatalf("expected old state machine at 1 to be removed")
	}
}

func TestCurrentBlockNumberOrZero(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(42)}}
	if got := currentBlockNumberOrZero(chain); got != 42 {
		t.Fatalf("currentBlockNumberOrZero() = %d, want 42", got)
	}

	nilChain := &initialSyncChainStub{current: &initialSyncBlockStub{}}
	if got := currentBlockNumberOrZero(nilChain); got != 0 {
		t.Fatalf("currentBlockNumberOrZero() with nil number = %d, want 0", got)
	}
}

func TestRequireBlockNumberNilBlock(t *testing.T) {
	if _, err := requireBlockNumber(nil, "msg"); err == nil {
		t.Fatalf("expected error for nil block")
	}
}

func TestRequireCurrentBlockNumberNilChain(t *testing.T) {
	if _, err := requireCurrentBlockNumber(nil, "msg"); err == nil {
		t.Fatalf("expected error for nil chain")
	}
}
