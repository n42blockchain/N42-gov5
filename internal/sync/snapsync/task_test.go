package snapsync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestRangeTaskIsAssigned(t *testing.T) {
	task := &RangeTask{}
	if task.IsAssigned() {
		t.Fatalf("expected not assigned initially")
	}
	task.Assigned = peer.ID("peer-1")
	if !task.IsAssigned() {
		t.Fatalf("expected assigned after setting Assigned")
	}
}

func TestRangeTaskIsTimedOut(t *testing.T) {
	task := &RangeTask{}
	if task.IsTimedOut() {
		t.Fatalf("expected not timed out when unassigned")
	}

	task.Assigned = peer.ID("peer-1")
	task.AssignedAt = time.Now()
	if task.IsTimedOut() {
		t.Fatalf("expected not timed out immediately after assignment")
	}

	task.AssignedAt = time.Now().Add(-2 * taskTimeout)
	if !task.IsTimedOut() {
		t.Fatalf("expected timed out after exceeding taskTimeout")
	}
}

func TestRangeTaskCanRetry(t *testing.T) {
	task := &RangeTask{}
	for i := 0; i < maxTaskRetries; i++ {
		if !task.CanRetry() {
			t.Fatalf("expected CanRetry true at retry %d", i)
		}
		task.Retries++
	}
	if task.CanRetry() {
		t.Fatalf("expected CanRetry false after reaching maxTaskRetries")
	}
}

func TestRangeTaskReset(t *testing.T) {
	task := &RangeTask{Assigned: peer.ID("peer-1"), AssignedAt: time.Now()}
	task.Reset()
	if task.IsAssigned() {
		t.Fatalf("expected not assigned after Reset")
	}
	if !task.AssignedAt.IsZero() {
		t.Fatalf("expected zero AssignedAt after Reset")
	}
}
