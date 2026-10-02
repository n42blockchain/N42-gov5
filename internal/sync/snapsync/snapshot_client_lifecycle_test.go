package snapsync

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

// TestSnapshotManagerRunContextDone covers Run()'s ctx.Done() branch,
// mirroring TestManagerRunContextDone for the snapshot-based manager.
func TestSnapshotManagerRunContextDone(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	sm := NewSnapshotManager(&conf.SnapSyncConfig{}, p2pStub, db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sm.Run(ctx, 500); err != ctx.Err() {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}

// TestSnapshotManagerPickPeerNoConnected covers pickPeer()'s
// empty-connected-peers branch.
func TestSnapshotManagerPickPeerNoConnected(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	sm := NewSnapshotManager(&conf.SnapSyncConfig{}, p2pStub, db)

	if pid := sm.pickPeer(); pid != "" {
		t.Fatalf("pickPeer() = %q, want empty with no connected peers", pid)
	}
}

// TestSnapshotManagerProcessTimeoutsResetsStaleTask covers
// processTimeouts()'s reset-on-timeout branch.
func TestSnapshotManagerProcessTimeoutsResetsStaleTask(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	sm := NewSnapshotManager(&conf.SnapSyncConfig{}, p2pStub, db)

	stale := &RangeTask{Kind: TaskAccount, Assigned: "peer-x", AssignedAt: time.Now().Add(-time.Hour)}
	sm.accountTasks = []*RangeTask{stale}

	sm.processTimeouts()

	if stale.Retries != 1 {
		t.Fatalf("expected stale task to be retried once, got %d", stale.Retries)
	}
	if stale.IsAssigned() {
		t.Fatalf("expected stale task to be reset (unassigned)")
	}
}

// TestSnapshotManagerFailTask covers failTask().
func TestSnapshotManagerFailTask(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	sm := NewSnapshotManager(&conf.SnapSyncConfig{}, p2pStub, db)

	task := &RangeTask{Kind: TaskCode, Assigned: "peer-z"}
	sm.failTask(task)

	if task.Retries != 1 {
		t.Fatalf("expected Retries=1 after failTask, got %d", task.Retries)
	}
	if task.IsAssigned() {
		t.Fatalf("expected task to be unassigned after failTask")
	}
}

// TestSnapshotManagerPickTaskPriority covers pickTask()'s account > storage >
// code priority ordering.
func TestSnapshotManagerPickTaskPriority(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	sm := NewSnapshotManager(&conf.SnapSyncConfig{}, p2pStub, db)

	storageTask := &RangeTask{Kind: TaskStorage}
	codeTask := &RangeTask{Kind: TaskCode}
	accountTask := &RangeTask{Kind: TaskAccount}
	sm.storageTasks = []*RangeTask{storageTask}
	sm.codeTasks = []*RangeTask{codeTask}
	sm.accountTasks = []*RangeTask{accountTask}

	if got := sm.pickTask(); got != accountTask {
		t.Fatalf("pickTask() = %v, want the account task (highest priority)", got)
	}

	accountTask.Retries = maxTaskRetries
	if got := sm.pickTask(); got != storageTask {
		t.Fatalf("pickTask() = %v, want the storage task once accounts are exhausted", got)
	}
}
