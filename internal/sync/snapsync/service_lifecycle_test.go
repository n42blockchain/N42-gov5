package snapsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
)

// TestServiceStartDisabledSyncsImmediately covers Start()/run()'s fast path
// when snap sync is disabled: the service should mark itself synced without
// touching the DB or peers.
func TestServiceStartDisabledSyncsImmediately(t *testing.T) {
	db := testDB(t)
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	svc := NewService(context.Background(), &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{Enable: false},
	})

	if svc.Synced() {
		t.Fatalf("expected not synced before Start()")
	}

	done := make(chan struct{})
	go func() {
		svc.Start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Start() to return")
	}

	if !svc.Synced() {
		t.Fatalf("expected Synced() true once snap sync is disabled")
	}
	if svc.Syncing() {
		t.Fatalf("expected Syncing() false once disabled run() returns")
	}
	if svc.Status() != nil {
		t.Fatalf("Status() = %v, want nil once synced", svc.Status())
	}
	if err := svc.Resync(); err != nil {
		t.Fatalf("Resync() error = %v, want nil (no-op)", err)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// A second Start() call must be a no-op (sync.Once) and not block.
	done2 := make(chan struct{})
	go func() {
		svc.Start()
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out on second Start() call")
	}
}

// TestServiceStartInvalidConfigSyncsImmediately covers run()'s
// SnapSync.Validate() failure branch.
func TestServiceStartInvalidConfigSyncsImmediately(t *testing.T) {
	db := testDB(t)
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	svc := NewService(context.Background(), &Config{
		P2P:   p2pStub,
		Chain: chain,
		DB:    db,
		// PivotDistance way above any sane bound makes Validate() fail.
		SnapSync: &conf.SnapSyncConfig{Enable: true, PivotDistance: 1 << 40},
	})

	done := make(chan struct{})
	go func() {
		svc.Start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Start() to return")
	}

	if !svc.Synced() {
		t.Fatalf("expected Synced() true once config validation fails")
	}
}

// TestServiceStartAlreadyHasState covers run()'s "node already has state"
// skip branch (current block > 0).
func TestServiceStartAlreadyHasState(t *testing.T) {
	db := testDB(t)
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{number: uint256.NewInt(42)}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	svc := NewService(context.Background(), &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{Enable: true},
	})

	done := make(chan struct{})
	go func() {
		svc.Start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Start() to return")
	}

	if !svc.Synced() {
		t.Fatalf("expected Synced() true once the node already has state")
	}
}

// TestServiceStartPreviousCompletion covers run()'s "already completed"
// skip branch via checkPreviousCompletion().
func TestServiceStartPreviousCompletion(t *testing.T) {
	db := testDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveState(tx, StateCompleted)
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	svc := NewService(context.Background(), &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{Enable: true},
	})

	done := make(chan struct{})
	go func() {
		svc.Start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Start() to return")
	}

	if !svc.Synced() {
		t.Fatalf("expected Synced() true once a previous run already completed")
	}
}

// TestServiceSelectPivotUsesCheckpoint covers selectPivot()'s short-circuit
// when a checkpoint pivot block is already stored.
func TestServiceSelectPivotUsesCheckpoint(t *testing.T) {
	db := testDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePivotBlock(tx, 999)
	}); err != nil {
		t.Fatalf("SavePivotBlock: %v", err)
	}

	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	svc := NewService(context.Background(), &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{},
	})

	pivot, stateRoot, err := svc.selectPivot()
	if err != nil {
		t.Fatalf("selectPivot() error = %v", err)
	}
	if pivot != 999 {
		t.Fatalf("selectPivot() pivot = %d, want 999", pivot)
	}
	if stateRoot != nil {
		t.Fatalf("selectPivot() stateRoot = %v, want nil for checkpoint pivot", stateRoot)
	}
}

// TestServiceSelectPivotContextDone covers selectPivot()'s ctx.Done() exit
// from its peer-waiting loop when no peers ever satisfy the requirement.
func TestServiceSelectPivotContextDone(t *testing.T) {
	db := testDB(t)
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	svc := NewService(ctx, &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{MinSnapPeers: 1},
	})

	errCh := make(chan error, 1)
	go func() {
		_, _, err := svc.selectPivot()
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("selectPivot() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for selectPivot to observe context cancellation")
	}
}

// TestServiceTrySnapshotSyncContextDone covers trySnapshotSync()'s
// ctx.Done() early return from its peer-waiting loop.
func TestServiceTrySnapshotSyncContextDone(t *testing.T) {
	db := testDB(t)
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	svc := NewService(ctx, &Config{
		P2P:      p2pStub,
		Chain:    chain,
		DB:       db,
		SnapSync: &conf.SnapSyncConfig{MinSnapPeers: 1},
	})

	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- svc.trySnapshotSync()
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case ok := <-resultCh:
		if ok {
			t.Fatalf("trySnapshotSync() = true, want false on context cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for trySnapshotSync to observe context cancellation")
	}
}
