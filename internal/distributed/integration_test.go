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

	// 4. Submit proof
	proof := []byte("stark-proof-data-for-fibonacci-10")
	outputs := []byte(`{"result": 55}`)
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
	taskID, _ := coprocSvc.SubmitTask(ph, []byte("data"), submitter)

	// Submit proof
	coprocSvc.SubmitProof(taskID, []byte("proof"), []byte("output"))

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
