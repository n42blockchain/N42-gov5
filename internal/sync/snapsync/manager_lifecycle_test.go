package snapsync

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

// TestManagerRunContextDone covers Run()'s ctx.Done() branch: resume() finds
// no prior progress (fresh memdb), initFresh() seeds one account task, then
// the select immediately observes the already-cancelled context and returns.
func TestManagerRunContextDone(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	mgr := NewManager(&conf.SnapSyncConfig{}, p2pStub, db, 100, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := mgr.Run(ctx); err != ctx.Err() {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}

// TestManagerPickPeerNoConnected covers pickPeer()'s empty-connected-peers
// branch.
func TestManagerPickPeerNoConnected(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	mgr := NewManager(&conf.SnapSyncConfig{}, p2pStub, db, 100, nil)

	if pid := mgr.pickPeer(); pid != "" {
		t.Fatalf("pickPeer() = %q, want empty with no connected peers", pid)
	}
}

// TestManagerProcessTimeoutsResetsStaleTask covers processTimeouts()'s
// reset-on-timeout branch for each task kind.
func TestManagerProcessTimeoutsResetsStaleTask(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	mgr := NewManager(&conf.SnapSyncConfig{}, p2pStub, db, 100, nil)

	stale := &RangeTask{Kind: TaskAccount, Assigned: "peer-x", AssignedAt: time.Now().Add(-time.Hour)}
	fresh := &RangeTask{Kind: TaskStorage, Assigned: "peer-y", AssignedAt: time.Now()}
	mgr.accountTasks = []*RangeTask{stale}
	mgr.storageTasks = []*RangeTask{fresh}

	mgr.processTimeouts()

	if stale.Retries != 1 {
		t.Fatalf("expected stale task to be retried once, got %d", stale.Retries)
	}
	if stale.IsAssigned() {
		t.Fatalf("expected stale task to be reset (unassigned)")
	}
	if fresh.Retries != 0 || !fresh.IsAssigned() {
		t.Fatalf("expected fresh task to be left untouched")
	}
}

// TestManagerFailTaskIncrementsRetriesAndResets covers failTask().
func TestManagerFailTaskIncrementsRetriesAndResets(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	mgr := NewManager(&conf.SnapSyncConfig{}, p2pStub, db, 100, nil)

	task := &RangeTask{Kind: TaskCode, Assigned: "peer-z"}
	mgr.failTask(task)

	if task.Retries != 1 {
		t.Fatalf("expected Retries=1 after failTask, got %d", task.Retries)
	}
	if task.IsAssigned() {
		t.Fatalf("expected task to be unassigned after failTask")
	}
}

// TestManagerPickTaskPriorityAndStuck covers pickTask()'s account > storage >
// code priority ordering and the stuck() detector once retries are exhausted.
func TestManagerPickTaskPriorityAndStuck(t *testing.T) {
	db := testDB(t)
	p2pStub := newSnapsyncFakeP2P(t, &conf.P2PConfig{})
	mgr := NewManager(&conf.SnapSyncConfig{}, p2pStub, db, 100, nil)

	storageTask := &RangeTask{Kind: TaskStorage}
	codeTask := &RangeTask{Kind: TaskCode}
	accountTask := &RangeTask{Kind: TaskAccount}
	mgr.storageTasks = []*RangeTask{storageTask}
	mgr.codeTasks = []*RangeTask{codeTask}
	mgr.accountTasks = []*RangeTask{accountTask}

	if got := mgr.pickTask(); got != accountTask {
		t.Fatalf("pickTask() = %v, want the account task (highest priority)", got)
	}

	if mgr.stuck() {
		t.Fatalf("expected not stuck while a task can still be assigned/retried")
	}

	// Exhaust retries on every task: stuck() should now report true.
	for _, tsk := range []*RangeTask{storageTask, codeTask, accountTask} {
		tsk.Retries = maxTaskRetries
	}
	if !mgr.stuck() {
		t.Fatalf("expected stuck once every task has exhausted its retries")
	}
	if got := mgr.pickTask(); got != nil {
		t.Fatalf("pickTask() = %v, want nil once retries are exhausted", got)
	}
}
