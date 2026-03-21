package coprocessor

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestChallengeSubmitAndResolve(t *testing.T) {
	cm := NewChallengeManager(3600)
	taskID := types.HexToHash("0x1111")
	challenger := types.HexToAddress("0x2222")

	cID, err := cm.Submit(taskID, challenger, 1000, "result seems wrong")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	c, ok := cm.Get(cID)
	if !ok {
		t.Fatal("challenge not found")
	}
	if c.Status != ChallengePending {
		t.Fatalf("status = %v, want Pending", c.Status)
	}
	if c.Bond != 1000 {
		t.Fatalf("bond = %d, want 1000", c.Bond)
	}

	// Resolve as upheld
	if err := cm.Resolve(cID, true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	c, _ = cm.Get(cID)
	if c.Status != ChallengeUpheld {
		t.Fatalf("status = %v, want Upheld", c.Status)
	}
}

func TestChallengeDuplicate(t *testing.T) {
	cm := NewChallengeManager(3600)
	taskID := types.HexToHash("0x1111")
	challenger := types.HexToAddress("0x2222")

	cm.Submit(taskID, challenger, 1000, "first")
	_, err := cm.Submit(taskID, challenger, 2000, "second")
	if err != ErrDuplicateChallenge {
		t.Fatalf("expected ErrDuplicateChallenge, got %v", err)
	}
}

func TestChallengeResolvedTwice(t *testing.T) {
	cm := NewChallengeManager(3600)
	taskID := types.HexToHash("0x3333")
	challenger := types.HexToAddress("0x4444")

	cID, _ := cm.Submit(taskID, challenger, 1000, "test")
	cm.Resolve(cID, false) // reject

	if err := cm.Resolve(cID, true); err != ErrChallengeResolved {
		t.Fatalf("expected ErrChallengeResolved, got %v", err)
	}
}

func TestChallengeGetByTask(t *testing.T) {
	cm := NewChallengeManager(3600)
	taskID := types.HexToHash("0x5555")

	cm.Submit(taskID, types.HexToAddress("0xa1"), 100, "r1")
	cm.Submit(taskID, types.HexToAddress("0xa2"), 200, "r2")

	challenges := cm.GetByTask(taskID)
	if len(challenges) != 2 {
		t.Fatalf("got %d challenges, want 2", len(challenges))
	}
}

func TestChallengeHasPending(t *testing.T) {
	cm := NewChallengeManager(3600)
	taskID := types.HexToHash("0x6666")
	challenger := types.HexToAddress("0xb1")

	if cm.HasPendingChallenge(taskID) {
		t.Fatal("should have no pending challenges")
	}

	cID, _ := cm.Submit(taskID, challenger, 100, "test")
	if !cm.HasPendingChallenge(taskID) {
		t.Fatal("should have pending challenge")
	}

	cm.Resolve(cID, false)
	if cm.HasPendingChallenge(taskID) {
		t.Fatal("should have no pending challenges after resolution")
	}
}

func TestChallengeExpireWindow(t *testing.T) {
	cm := NewChallengeManager(3600)

	// Task with expired challenge window and no pending challenges
	pastDeadline := time.Now().Add(-1 * time.Hour)
	tasks := []*Task{
		{
			ID:                types.HexToHash("0x7777"),
			Status:            TaskOptimisticVerified,
			ChallengeDeadline: pastDeadline,
		},
		{
			ID:                types.HexToHash("0x8888"),
			Status:            TaskOptimisticVerified,
			ChallengeDeadline: time.Now().Add(1 * time.Hour), // not expired
		},
	}

	ready := cm.ExpireWindow(tasks)
	if len(ready) != 1 {
		t.Fatalf("got %d ready tasks, want 1", len(ready))
	}
	if ready[0] != tasks[0].ID {
		t.Fatal("wrong task ID")
	}
}

func TestServiceSubmitChallenge(t *testing.T) {
	cfg := testConfig()
	cfg.OptimisticChallengeSec = 3600
	svc, _ := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	ph := types.HexToHash("0xee")
	svc.Registry().Register(ph, []byte("vk"), "test-prog")

	taskID, _ := svc.SubmitTask(ph, []byte("input"), types.Address{})

	// Set optimistic tier and bond
	svc.Tasks().mu.Lock()
	task := svc.Tasks().tasks[taskID]
	task.VerificationTier = TierOptimistic
	task.Bond = 1_000_000
	svc.Tasks().mu.Unlock()

	svc.SubmitProof(taskID, []byte("proof"), []byte("output"))

	// Submit challenge
	challenger := types.HexToAddress("0xcc")
	challengeID, err := svc.SubmitChallenge(taskID, challenger, 500, "dispute")
	if err != nil {
		t.Fatalf("SubmitChallenge: %v", err)
	}

	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != TaskChallenged {
		t.Fatalf("status = %v, want Challenged", task.Status)
	}

	c, ok := svc.Challenges().Get(challengeID)
	if !ok {
		t.Fatal("challenge not found")
	}
	if c.Bond != 500 {
		t.Fatalf("challenge bond = %d, want 500", c.Bond)
	}
}

func TestChallengePrune(t *testing.T) {
	cm := NewChallengeManager(3600)

	taskID1 := types.HexToHash("0xaaaa")
	taskID2 := types.HexToHash("0xbbbb")
	challenger := types.HexToAddress("0x1234")

	// Submit two challenges
	cID1, _ := cm.Submit(taskID1, challenger, 100, "resolved one")
	cID2, _ := cm.Submit(taskID2, challenger, 200, "pending one")

	// Resolve first challenge so it becomes eligible for pruning
	cm.Resolve(cID1, false) // rejected

	// Wait briefly so ResolvedAt is strictly before the prune cutoff
	time.Sleep(2 * time.Millisecond)

	// Prune challenges older than 1ms — the resolved one qualifies, the pending one does not
	pruned := cm.Prune(1 * time.Millisecond)
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	// cID1 should be gone, cID2 (pending) should remain
	_, ok1 := cm.Get(cID1)
	if ok1 {
		t.Fatal("resolved challenge should have been pruned")
	}
	_, ok2 := cm.Get(cID2)
	if !ok2 {
		t.Fatal("pending challenge should NOT be pruned")
	}
}

func TestChallengeCount(t *testing.T) {
	cm := NewChallengeManager(3600)

	if cm.Count() != 0 {
		t.Fatalf("count = %d, want 0", cm.Count())
	}

	cm.Submit(types.HexToHash("0x1111"), types.HexToAddress("0xa1"), 100, "r1")
	cm.Submit(types.HexToHash("0x2222"), types.HexToAddress("0xa2"), 200, "r2")

	if cm.Count() != 2 {
		t.Fatalf("count = %d, want 2", cm.Count())
	}
}

func TestChallengeResolveNotFound(t *testing.T) {
	cm := NewChallengeManager(3600)

	// Resolve with a hash that doesn't exist
	err := cm.Resolve(types.HexToHash("0xdeadbeef"), true)
	if err != ErrChallengeNotFound {
		t.Fatalf("expected ErrChallengeNotFound, got %v", err)
	}
}
