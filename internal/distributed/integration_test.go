// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package distributed provides integration tests for the distributed
// infrastructure subsystems (coprocessor, messaging, storage, notify).
// These tests exercise cross-module interactions and end-to-end flows.
package distributed

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
	"github.com/n42blockchain/N42/internal/distributed/messaging"
	"github.com/n42blockchain/N42/internal/distributed/notify"
)

// TestCoprocessorEndToEnd exercises the full task lifecycle:
// register program → submit task → submit proof → verify → prune.
func TestCoprocessorEndToEnd(t *testing.T) {
	cfg := conf.DefaultCoprocessorCfg()
	cfg.Enabled = true
	cfg.TaskTimeoutSec = 5
	cfg.PruneIntervalSec = 1

	svc, err := coprocessor.NewService(&cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()
	defer svc.Stop()

	// 1. Register a program
	programHash := types.HexToHash("0xabcdef")
	vk := []byte("verification-key-for-test-program-v1")
	if err := svc.Registry().Register(programHash, vk, "fibonacci"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// ZK tier is fail-closed: wire a backend that accepts the blob.
	if err := svc.Verifier().SetZKBlobVerifier(func(*coprocessor.Program, []byte, types.Hash, types.Hash) (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatalf("SetZKBlobVerifier: %v", err)
	}

	// 2. Submit a task
	input := []byte(`{"n": 10}`)
	submitter := types.HexToAddress("0x1234")
	taskID, err := svc.SubmitTask(programHash, input, submitter)
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}
	t.Logf("Task submitted: %s", taskID.Hex()[:16])

	// 3. Verify task is pending
	task, ok := svc.Tasks().GetTask(taskID)
	if !ok || task.Status != coprocessor.TaskPending {
		t.Fatalf("task should be pending, got %v", task)
	}

	// 4. Submit proof: the ZK tier requires the v1 envelope binding this
	// task's program, input and claimed outputs.
	outputs := []byte(`{"result": 55}`)
	proof := coprocessor.BuildZKProofEnvelope(task.ID, programHash, task.InputHash, outputs,
		[]byte("stark-proof-data-for-fibonacci-10"))
	verified, err := svc.SubmitProof(taskID, proof, outputs)
	if err != nil {
		t.Fatalf("SubmitProof: %v", err)
	}
	if !verified {
		t.Fatal("proof should be verified")
	}

	// 5. Verify task is now verified
	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified", task.Status)
	}
	if string(task.PublicOutputs) != `{"result": 55}` {
		t.Fatalf("outputs = %s", task.PublicOutputs)
	}

	// 6. Empty proof should be rejected
	taskID2, _ := svc.SubmitTask(programHash, []byte("x"), submitter)
	_, err = svc.SubmitProof(taskID2, nil, nil)
	if err == nil {
		t.Fatal("empty proof should be rejected")
	}

	// 7. Unregistered program should be rejected
	_, err = svc.SubmitTask(types.HexToHash("0x999"), []byte("y"), submitter)
	if err == nil {
		t.Fatal("unregistered program should be rejected")
	}

	t.Logf("Coprocessor e2e: programs=%d, pending=%d, total=%d",
		svc.Registry().Count(), svc.Tasks().PendingCount(), svc.Tasks().TotalCount())
}

// TestMessagingEndToEnd exercises publish → subscribe → retrieve flow.
func TestMessagingEndToEnd(t *testing.T) {
	cfg := conf.DefaultMessagingCfg()
	cfg.Enabled = true
	cfg.StoreCapacity = 50
	cfg.StoreTTLSec = 10
	cfg.RLNRateLimit = 100

	svc := messaging.NewService(&cfg)
	svc.Start()
	defer svc.Stop()

	// 1. Subscribe to a topic
	received := make(chan *messaging.Message, 10)
	svc.Subscribe("chat/general", func(msg *messaging.Message) {
		received <- msg
	})

	// 2. Publish messages
	for i := 0; i < 5; i++ {
		id, err := svc.Publish("chat/general", []byte("hello "+string(rune('0'+i))), "text/plain", "peer-A")
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if id == ([32]byte{}) {
			t.Fatal("expected non-zero message ID")
		}
	}

	// 3. Verify handler received messages
	time.Sleep(50 * time.Millisecond)
	if len(received) != 5 {
		t.Fatalf("received %d messages, want 5", len(received))
	}

	// 4. Retrieve from store
	msgs := svc.GetMessages("chat/general", time.Time{}, time.Time{}, 10)
	if len(msgs) != 5 {
		t.Fatalf("store has %d messages, want 5", len(msgs))
	}

	// 5. Publish to different topic
	svc.Publish("chat/private", []byte("secret"), "text/plain", "peer-B")
	msgs = svc.GetMessages("chat/private", time.Time{}, time.Time{}, 10)
	if len(msgs) != 1 {
		t.Fatalf("private topic has %d messages, want 1", len(msgs))
	}

	// 6. Stats
	stats := svc.Stats()
	if stats["published"].(uint64) != 6 {
		t.Fatalf("published = %v, want 6", stats["published"])
	}

	t.Logf("Messaging e2e: %v", stats)
}

// TestNotifyEndToEnd exercises subscribe → dispatch → history flow.
func TestNotifyEndToEnd(t *testing.T) {
	cfg := conf.DefaultNotifyCfg()
	cfg.Enabled = true
	cfg.MaxHistory = 10
	cfg.MaxSubscribers = 100
	cfg.BufferSize = 32

	svc := notify.NewService(&cfg)
	svc.Start()
	defer svc.Stop()

	addr1 := types.HexToAddress("0xaaaa")
	addr2 := types.HexToAddress("0xbbbb")
	topic1 := types.HexToHash("0x1111")

	// 1. Subscribe to addr1
	ch1, err := svc.Subscribe(notify.SubscriptionFilter{
		Addresses: []types.Address{addr1},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 2. Subscribe to topic1 (any address)
	ch2, err := svc.Subscribe(notify.SubscriptionFilter{
		Topics: []types.Hash{topic1},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 3. Dispatch notification matching addr1 + topic1
	n1 := &notify.Notification{
		ID:          types.HexToHash("0x01"),
		Address:     addr1,
		Topic:       topic1,
		Data:        []byte("transfer event"),
		BlockNumber: 100,
		Timestamp:   time.Now(),
	}
	svc.Dispatch(n1)

	// 4. Dispatch notification matching only addr2 (ch1 should NOT get it)
	n2 := &notify.Notification{
		ID:          types.HexToHash("0x02"),
		Address:     addr2,
		Topic:       topic1,
		Data:        []byte("other event"),
		BlockNumber: 101,
		Timestamp:   time.Now(),
	}
	svc.Dispatch(n2)

	// 5. ch1 should receive only n1 (addr1 match)
	select {
	case got := <-ch1.Receive():
		if got.BlockNumber != 100 {
			t.Fatalf("ch1 got block %d, want 100", got.BlockNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("ch1 timeout")
	}

	// ch1 should NOT receive n2
	select {
	case got := <-ch1.Receive():
		t.Fatalf("ch1 should not get addr2 notification, got block %d", got.BlockNumber)
	case <-time.After(50 * time.Millisecond):
		// OK
	}

	// 6. ch2 should receive both (topic1 match)
	count := 0
	for i := 0; i < 2; i++ {
		select {
		case <-ch2.Receive():
			count++
		case <-time.After(time.Second):
			break
		}
	}
	if count != 2 {
		t.Fatalf("ch2 received %d, want 2", count)
	}

	// 7. Check history
	hist := svc.History(addr1, 10)
	if len(hist) != 1 {
		t.Fatalf("addr1 history = %d, want 1", len(hist))
	}
	hist2 := svc.History(addr2, 10)
	if len(hist2) != 1 {
		t.Fatalf("addr2 history = %d, want 1", len(hist2))
	}

	// 8. Stats
	stats := svc.Stats()
	t.Logf("Notify e2e: %v", stats)
}

// TestTieredVerificationEndToEnd exercises the optimistic verification flow:
// register program → submit task → set optimistic tier + bond → submit proof →
// verify optimistic-verified state → wait for challenge window → verify finalized.
func TestTieredVerificationEndToEnd(t *testing.T) {
	cfg := conf.DefaultCoprocessorCfg()
	cfg.Enabled = true
	cfg.TaskTimeoutSec = 30
	cfg.OptimisticChallengeSec = 1
	cfg.PruneIntervalSec = 1

	svc, err := coprocessor.NewService(&cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()

	// 1. Register a program
	programHash := types.HexToHash("0xdeadbeef01")
	vk := []byte("vk-optimistic-test")
	if err := svc.Registry().Register(programHash, vk, "optimistic-program"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 2. Submit a task
	submitter := types.HexToAddress("0xaaaa01")
	taskID, err := svc.SubmitTask(programHash, []byte("compute-input"), submitter)
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	// 3. Set optimistic tier and bond on the task before proof submission.
	// GetTask returns a pointer to the live task struct; fields are exported.
	task, ok := svc.Tasks().GetTask(taskID)
	if !ok {
		t.Fatal("task not found after submission")
	}
	task.VerificationTier = coprocessor.TierOptimistic
	task.Bond = 1_000_000_000_000_000_000 // 1 ETH bond

	// 4. Submit proof — should result in TaskOptimisticVerified, not TaskVerified.
	// The optimistic "proof" IS the claimed result, so both copies must match.
	outputs := []byte(`{"result": "optimistic"}`)
	verified, err := svc.SubmitProof(taskID, outputs, outputs)
	if err != nil {
		t.Fatalf("SubmitProof: %v", err)
	}
	if !verified {
		t.Fatal("optimistic proof should be accepted")
	}

	// 5. Verify task is in optimistic-verified state (not yet finalized)
	// Use GetTaskSnapshot to avoid racing with the maintenance goroutine.
	snap, _ := svc.Tasks().GetTaskSnapshot(taskID)
	if snap.Status != coprocessor.TaskOptimisticVerified {
		t.Fatalf("status = %v, want OptimisticVerified", snap.Status)
	}
	if snap.ChallengeDeadline.IsZero() {
		t.Fatal("challenge deadline should be set")
	}
	t.Logf("Task optimistic-verified, deadline: %v", snap.ChallengeDeadline)

	// 6. Wait for challenge window (1s) + maintenance tick (1s) + buffer
	time.Sleep(3 * time.Second)

	// 7. Stop service to prevent concurrent writes before reading task
	svc.Stop()

	// 8. Verify task is now finalized to Verified
	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified after challenge window expired", task.Status)
	}

	t.Logf("Tiered verification e2e: task finalized from OptimisticVerified → Verified")
}

// TestProviderMarketplaceEndToEnd exercises the provider lifecycle:
// register provider → submit task → provider claims task → submit proof →
// verify provider was rewarded.
func TestProviderMarketplaceEndToEnd(t *testing.T) {
	cfg := conf.DefaultCoprocessorCfg()
	cfg.Enabled = true
	cfg.TaskTimeoutSec = 30
	cfg.MinProviderStake = 100 // low stake for testing
	cfg.EnableMarketplace = true

	svc, err := coprocessor.NewService(&cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()
	defer svc.Stop()

	// 1. Register a program
	programHash := types.HexToHash("0xdeadbeef02")
	if err := svc.Registry().Register(programHash, []byte("vk-marketplace"), "marketplace-program"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := svc.Verifier().SetZKBlobVerifier(func(*coprocessor.Program, []byte, types.Hash, types.Hash) (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatalf("SetZKBlobVerifier: %v", err)
	}

	// 2. Register a provider with sufficient stake
	providerAddr := types.HexToAddress("0xABCDEF1234567890ABcdef1234567890AbCdEf12")
	capabilities := []coprocessor.Capability{coprocessor.CapZK, coprocessor.CapGeneral}
	if err := svc.RegisterProvider(providerAddr, 1000, capabilities); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	// 3. Verify provider is active
	provider, ok := svc.Providers().Get(providerAddr)
	if !ok {
		t.Fatal("provider not found after registration")
	}
	if provider.Status != coprocessor.ProviderActive {
		t.Fatalf("provider status = %v, want Active", provider.Status)
	}
	if !provider.HasCapability(coprocessor.CapZK) {
		t.Fatal("provider should have ZK capability")
	}
	t.Logf("Provider registered: stake=%d, reputation=%d", provider.Stake, provider.Reputation)

	// 4. Submit a task
	submitter := types.HexToAddress("0x2222222222222222222222222222222222222222")
	taskID, err := svc.SubmitTask(programHash, []byte("marketplace-input"), submitter)
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	// 5. Provider claims the task
	if err := svc.ClaimTask(taskID, providerAddr); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// 6. Verify task is assigned to the provider
	task, ok := svc.Tasks().GetTask(taskID)
	if !ok {
		t.Fatal("task not found")
	}
	if task.AssignedProvider != providerAddr {
		t.Fatalf("assigned provider = %v, want %v", task.AssignedProvider, providerAddr)
	}

	// 7. Set a reward amount and submit proof (TierZK by default)
	task.RewardAmount = 500
	outputs := []byte(`{"result": "done"}`)
	proof := coprocessor.BuildZKProofEnvelope(task.ID, programHash, task.InputHash, outputs,
		[]byte("zk-proof-for-marketplace-task"))
	verified, err := svc.SubmitProof(taskID, proof, outputs)
	if err != nil {
		t.Fatalf("SubmitProof: %v", err)
	}
	if !verified {
		t.Fatal("proof should be verified")
	}

	// 8. Verify task completed successfully
	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified", task.Status)
	}

	// 9. Verify provider was rewarded (reputation increased, task count incremented)
	provider, _ = svc.Providers().Get(providerAddr)
	if provider.Reputation <= 5000 {
		t.Fatalf("provider reputation = %d, expected increase above initial 5000", provider.Reputation)
	}
	if provider.TasksTotal == 0 {
		t.Fatal("provider task count should be > 0 after completing a task")
	}

	// 10. Unregister provider
	if err := svc.UnregisterProvider(providerAddr); err != nil {
		t.Fatalf("UnregisterProvider: %v", err)
	}
	if svc.Providers().TotalCount() != 0 {
		t.Fatal("provider count should be 0 after unregistering")
	}

	t.Logf("Provider marketplace e2e: provider rewarded, reputation=%d, tasks=%d",
		provider.Reputation, provider.TasksTotal)
}

// TestChallengeFlowEndToEnd exercises the challenge lifecycle:
// register program → submit task (optimistic) → submit proof →
// verify optimistic-verified → submit challenge → verify challenged status.
func TestChallengeFlowEndToEnd(t *testing.T) {
	cfg := conf.DefaultCoprocessorCfg()
	cfg.Enabled = true
	cfg.TaskTimeoutSec = 30
	cfg.OptimisticChallengeSec = 60 // long window so challenge arrives in time

	svc, err := coprocessor.NewService(&cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.Start()
	defer svc.Stop()

	// 1. Register a program
	programHash := types.HexToHash("0xdeadbeef03")
	if err := svc.Registry().Register(programHash, []byte("vk-challenge"), "challenge-program"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 2. Submit a task
	submitter := types.HexToAddress("0xsubmitter03")
	taskID, err := svc.SubmitTask(programHash, []byte("challenge-input"), submitter)
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	// 3. Set optimistic tier + bond before proof submission
	task, ok := svc.Tasks().GetTask(taskID)
	if !ok {
		t.Fatal("task not found")
	}
	task.VerificationTier = coprocessor.TierOptimistic
	task.Bond = 500_000_000_000_000_000 // 0.5 ETH

	// 4. Submit proof → should land in TaskOptimisticVerified. The claimed
	// result is the proof, so both copies must be identical.
	claimed := []byte(`{"val":42}`)
	verified, err := svc.SubmitProof(taskID, claimed, claimed)
	if err != nil {
		t.Fatalf("SubmitProof: %v", err)
	}
	if !verified {
		t.Fatal("optimistic proof should be accepted")
	}

	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != coprocessor.TaskOptimisticVerified {
		t.Fatalf("status = %v, want OptimisticVerified before challenge", task.Status)
	}

	// 5. Submit a challenge
	challenger := types.HexToAddress("0xchallenger03")
	challengeID, err := svc.SubmitChallenge(taskID, challenger, 100_000_000, "incorrect computation")
	if err != nil {
		t.Fatalf("SubmitChallenge: %v", err)
	}
	if challengeID == (types.Hash{}) {
		t.Fatal("challenge ID should be non-zero")
	}
	t.Logf("Challenge submitted: %s", challengeID.Hex()[:16])

	// 6. Verify task status is now Challenged
	task, _ = svc.Tasks().GetTask(taskID)
	if task.Status != coprocessor.TaskChallenged {
		t.Fatalf("status = %v, want Challenged", task.Status)
	}
	if task.Challenger != challenger {
		t.Fatalf("challenger = %v, want %v", task.Challenger, challenger)
	}

	// 7. Verify challenge exists in the challenge manager
	challenge, ok := svc.Challenges().Get(challengeID)
	if !ok {
		t.Fatal("challenge not found in manager")
	}
	if challenge.TaskID != taskID {
		t.Fatalf("challenge taskID = %v, want %v", challenge.TaskID, taskID)
	}
	if challenge.Status != coprocessor.ChallengePending {
		t.Fatalf("challenge status = %v, want Pending", challenge.Status)
	}
	if svc.Challenges().HasPendingChallenge(taskID) == false {
		t.Fatal("HasPendingChallenge should return true")
	}

	// 8. Verify challenge is returned by GetByTask
	challenges := svc.Challenges().GetByTask(taskID)
	if len(challenges) != 1 {
		t.Fatalf("GetByTask returned %d challenges, want 1", len(challenges))
	}
	if challenges[0].Challenger != challenger {
		t.Fatalf("challenge challenger = %v, want %v", challenges[0].Challenger, challenger)
	}

	t.Logf("Challenge flow e2e: task challenged, challenge count=%d", svc.Challenges().Count())
}

// TestCrossModuleIntegration verifies that coprocessor task completion
// can trigger a notification via the notify service.
func TestCrossModuleIntegration(t *testing.T) {
	// Set up coprocessor
	coprocessorCfg := conf.DefaultCoprocessorCfg()
	coprocessorCfg.Enabled = true
	coprocSvc, _ := coprocessor.NewService(&coprocessorCfg)
	coprocSvc.Start()
	defer coprocSvc.Stop()

	// Set up notify
	notifyCfg := conf.DefaultNotifyCfg()
	notifyCfg.Enabled = true
	notifySvc := notify.NewService(&notifyCfg)
	notifySvc.Start()
	defer notifySvc.Stop()

	// Subscribe to notifications from the submitter address
	submitter := types.HexToAddress("0x5555")
	ch, _ := notifySvc.Subscribe(notify.SubscriptionFilter{
		Addresses: []types.Address{submitter},
	})

	// Register program and submit task
	ph := types.HexToHash("0xbeef")
	coprocSvc.Registry().Register(ph, []byte("vk"), "test")
	if err := coprocSvc.Verifier().SetZKBlobVerifier(func(*coprocessor.Program, []byte, types.Hash, types.Hash) (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatalf("SetZKBlobVerifier: %v", err)
	}
	taskID, _ := coprocSvc.SubmitTask(ph, []byte("data"), submitter)

	// Submit proof (ZK tier: v1 envelope bound to the task)
	ctask, _ := coprocSvc.Tasks().GetTask(taskID)
	output := []byte("output")
	coprocSvc.SubmitProof(taskID, coprocessor.BuildZKProofEnvelope(ctask.ID, ph, ctask.InputHash, output, []byte("proof")), output)

	// Simulate: after proof verification, dispatch notification
	task, _ := coprocSvc.Tasks().GetTask(taskID)
	if task.Status == coprocessor.TaskVerified {
		notifySvc.Dispatch(&notify.Notification{
			ID:          taskID,
			Address:     submitter,
			Data:        task.PublicOutputs,
			BlockNumber: 200,
			Timestamp:   time.Now(),
		})
	}

	// Verify notification received
	select {
	case got := <-ch.Receive():
		if got.BlockNumber != 200 {
			t.Fatalf("notification block = %d, want 200", got.BlockNumber)
		}
		if string(got.Data) != "output" {
			t.Fatalf("notification data = %s, want output", got.Data)
		}
		t.Log("Cross-module integration: coprocessor → notify pipeline verified")
	case <-time.After(time.Second):
		t.Fatal("cross-module notification timeout")
	}
}
