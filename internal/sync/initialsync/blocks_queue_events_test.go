package initialsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	n42sync "github.com/n42blockchain/N42/internal/sync"
)

// newTestBlocksQueue builds a minimal blocksQueue (no network, no running
// loop) suitable for exercising the event-handler methods directly.
func newTestBlocksQueue(highest uint64) *blocksQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &blocksQueue{
		ctx:                    ctx,
		cancel:                 cancel,
		smm:                    newStateMachineManager(),
		blocksFetcher:          &blocksFetcher{blocksPerPeriod: 10},
		highestExpectedBlockNr: uint256.NewInt(highest),
		fetchedData:            make(chan *blocksQueueFetchedData, 1),
	}
}

func newTestBlocksQueueWithFetcher(t *testing.T, highest, currentBlockNr uint64) *blocksQueue {
	t.Helper()
	q := newTestBlocksQueue(highest)
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(currentBlockNr)}}
	q.chain = chain
	q.blocksFetcher = newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: chain,
		p2p:   newInitialsyncFakeP2P(t, nil),
	})
	t.Cleanup(func() {
		q.blocksFetcher.cancel()
		q.blocksFetcher.rateLimiter.Free()
	})
	return q
}

func TestBlocksQueueWaitHighestExpectedBlockNr(t *testing.T) {
	q := newTestBlocksQueueWithFetcher(t, 100, 0)
	if q.waitHighestExpectedBlockNr() {
		t.Fatalf("expected false when target not yet reached")
	}

	// Reached target, fetcher reports no higher finalized number: queue cancels.
	q2 := newTestBlocksQueueWithFetcher(t, 0, 0)
	if q2.waitHighestExpectedBlockNr() {
		t.Fatalf("expected false (no update) once target reached with nothing higher reported")
	}
	if q2.ctx.Err() == nil {
		t.Fatalf("expected queue context to be cancelled once target is reached")
	}
}

func TestBlocksQueueWaitHighestExpectedBlockNrCtxDone(t *testing.T) {
	q := newTestBlocksQueue(100)
	q.cancel()
	if q.waitHighestExpectedBlockNr() {
		t.Fatalf("expected false when ctx already done")
	}
}

func TestBlocksQueueOnScheduleEvent(t *testing.T) {
	q := newTestBlocksQueue(1000)
	q.blocksFetcher = newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}},
		p2p:   newInitialsyncFakeP2P(t, nil),
	})
	defer q.blocksFetcher.cancel()
	defer q.blocksFetcher.rateLimiter.Free()

	handler := q.onScheduleEvent(context.Background())
	m := &stateMachine{start: uint256.NewInt(0), state: stateNew}

	newState, err := handler(m, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateScheduled {
		t.Fatalf("expected stateScheduled, got %v", newState)
	}

	// Wrong initial state is rejected.
	m2 := &stateMachine{start: uint256.NewInt(0), state: stateSent}
	if _, err := handler(m2, nil); err != errInvalidInitialState {
		t.Fatalf("expected errInvalidInitialState, got %v", err)
	}

	// Start beyond the highest expected block number is skipped.
	m3 := &stateMachine{start: uint256.NewInt(5000), state: stateNew}
	newState, err = handler(m3, nil)
	if err != errBlockNrIsTooHigh {
		t.Fatalf("expected errBlockNrIsTooHigh, got %v", err)
	}
	if newState != stateSkipped {
		t.Fatalf("expected stateSkipped, got %v", newState)
	}
}

func TestBlocksQueueOnDataReceivedEvent(t *testing.T) {
	q := newTestBlocksQueue(1000)
	handler := q.onDataReceivedEvent(context.Background())

	// Wrong input type.
	m := &stateMachine{start: uint256.NewInt(0), state: stateScheduled}
	if _, err := handler(m, "not a response"); err != errInputNotFetchRequestParams {
		t.Fatalf("expected errInputNotFetchRequestParams, got %v", err)
	}

	// Wrong initial state.
	m2 := &stateMachine{start: uint256.NewInt(0), state: stateNew}
	if _, err := handler(m2, &fetchRequestResponse{}); err != errInvalidInitialState {
		t.Fatalf("expected errInvalidInitialState, got %v", err)
	}

	// Successful response moves to stateDataParsed.
	blocks := []*block.Block{{}}
	resp := &fetchRequestResponse{start: uint256.NewInt(0), blocks: blocks}
	newState, err := handler(m, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateDataParsed {
		t.Fatalf("expected stateDataParsed, got %v", newState)
	}
	if len(m.blocks) != 1 {
		t.Fatalf("expected blocks to be copied onto the state machine")
	}

	// Error response is propagated, state unchanged.
	m3 := &stateMachine{start: uint256.NewInt(0), state: stateScheduled}
	resp3 := &fetchRequestResponse{start: uint256.NewInt(0), err: n42sync.ErrInvalidFetchedData}
	q.blocksFetcher = newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}},
		p2p:   newInitialsyncFakeP2P(t, nil),
	})
	defer q.blocksFetcher.cancel()
	defer q.blocksFetcher.rateLimiter.Free()
	if _, err := handler(m3, resp3); err != n42sync.ErrInvalidFetchedData {
		t.Fatalf("expected ErrInvalidFetchedData, got %v", err)
	}
}

func TestBlocksQueueOnDataReceivedEventCtxDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := newTestBlocksQueue(1000)
	handler := q.onDataReceivedEvent(ctx)
	m := &stateMachine{start: uint256.NewInt(0), state: stateScheduled}
	if _, err := handler(m, &fetchRequestResponse{}); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestBlocksQueueOnReadyToSendEvent(t *testing.T) {
	q := newTestBlocksQueue(1000)
	handler := q.onReadyToSendEvent(context.Background())

	// Wrong initial state.
	m := &stateMachine{start: uint256.NewInt(0), state: stateNew}
	if _, err := handler(m, nil); err != errInvalidInitialState {
		t.Fatalf("expected errInvalidInitialState, got %v", err)
	}

	// No blocks -> skip.
	m2 := &stateMachine{start: uint256.NewInt(0), state: stateDataParsed}
	newState, err := handler(m2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateSkipped {
		t.Fatalf("expected stateSkipped for empty blocks, got %v", newState)
	}

	// First (lowest) machine with blocks sends immediately.
	q.smm.addStateMachine(uint256.NewInt(0))
	m3 := q.smm.machines[0]
	m3.state = stateDataParsed
	m3.blocks = []*block.Block{{}}
	newState, err = handler(m3, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateSent {
		t.Fatalf("expected stateSent, got %v", newState)
	}
	select {
	case <-q.fetchedData:
	default:
		t.Fatalf("expected data pushed to fetchedData channel")
	}
}

func TestBlocksQueueOnReadyToSendEventWaitsOnEarlierMachine(t *testing.T) {
	q := newTestBlocksQueue(1000)
	handler := q.onReadyToSendEvent(context.Background())

	q.smm.addStateMachine(uint256.NewInt(0))
	q.smm.addStateMachine(uint256.NewInt(10))
	earlier := q.smm.machines[0]
	earlier.state = stateNew // blocks progress of the later machine

	later := q.smm.machines[10]
	later.state = stateDataParsed
	later.blocks = []*block.Block{{}}

	newState, err := handler(later, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateDataParsed {
		t.Fatalf("expected state to remain stateDataParsed while waiting, got %v", newState)
	}
	select {
	case <-q.fetchedData:
		t.Fatalf("did not expect data to be sent while an earlier machine is unfinished")
	default:
	}
}

func TestBlocksQueueOnProcessSkippedEvent(t *testing.T) {
	q := newTestBlocksQueueWithFetcher(t, 1000, 0)
	handler := q.onProcessSkippedEvent(context.Background())

	// Wrong initial state.
	m := &stateMachine{start: uint256.NewInt(0), state: stateNew}
	if _, err := handler(m, nil); err != errInvalidInitialState {
		t.Fatalf("expected errInvalidInitialState, got %v", err)
	}

	// Single machine (so it is both first and last), all machines skipped,
	// and no peers report a higher finalized number than our current block:
	// the handler reports errNoRequiredPeers so the queue's loop can back off.
	q.smm.addStateMachine(uint256.NewInt(0))
	only := q.smm.machines[0]
	only.state = stateSkipped
	only.updated = time.Now()
	newState, err := handler(only, nil)
	if err != errNoRequiredPeers {
		t.Fatalf("expected errNoRequiredPeers, got %v", err)
	}
	if newState != stateSkipped {
		t.Fatalf("expected stateSkipped, got %v", newState)
	}
}

func TestBlocksQueueOnCheckStaleEvent(t *testing.T) {
	q := newTestBlocksQueue(1000)
	handler := q.onCheckStaleEvent(context.Background())

	m := &stateMachine{start: uint256.NewInt(0), state: stateNew}
	if _, err := handler(m, nil); err != errInvalidInitialState {
		t.Fatalf("expected errInvalidInitialState, got %v", err)
	}

	fresh := &stateMachine{start: uint256.NewInt(0), state: stateSent, updated: time.Now()}
	newState, err := handler(fresh, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateSent {
		t.Fatalf("expected state to remain stateSent while fresh, got %v", newState)
	}

	stale := &stateMachine{start: uint256.NewInt(0), state: stateSent, updated: time.Now().Add(-2 * staleEpochTimeout)}
	newState, err = handler(stale, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newState != stateSkipped {
		t.Fatalf("expected stateSkipped once stale, got %v", newState)
	}
}
