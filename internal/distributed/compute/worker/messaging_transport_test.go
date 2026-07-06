// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Integration tests for MessagingTransport against a REAL messaging.Service
// (real rate limiter, real store, real local dispatch — no relay, i.e. the
// single-node many-workers case). Proves the compute scheduler pipeline runs
// unchanged over the production transport, and that the cross-node relay path
// (Service.DeliverFromRelay) reaches the same handlers.

package worker

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
	"github.com/n42blockchain/N42/internal/distributed/messaging"
)

func newTestService(t *testing.T) *messaging.Service {
	t.Helper()
	cfg := conf.DefaultMessagingCfg()
	cfg.RLNRateLimit = 0         // unlimited: the scheduler bursts many attempts
	cfg.MaxMessageSize = 1 << 20 // 1 MiB, room for test bytecode
	svc := messaging.NewService(&cfg)
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}

// M1: remote execution over the real messaging service, signed round-trip.
func TestMessagingRemoteExecute(t *testing.T) {
	svc := newTestService(t)
	tr := NewMessagingTransport(svc)

	key, addr := genWorkerKey(t)
	w := NewWorker(key, echoRunner([]byte("via-gossip")), tr)
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(tr)
	out, err := re.Execute(context.Background(), addr, testSpec())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !bytes.Equal(out, []byte("via-gossip")) {
		t.Fatalf("output = %q", out)
	}
}

// M2: forged signatures are still rejected over the messaging transport
// (authenticity is the compute protocol's job, not the messaging layer's).
func TestMessagingForgedSignatureRejected(t *testing.T) {
	svc := newTestService(t)
	tr := NewMessagingTransport(svc)

	realKey, realAddr := genWorkerKey(t)
	wrongKey, _ := genWorkerKey(t)
	w := NewWorker(realKey, echoRunner([]byte("evil")), tr)
	w.overrideSigningKey(wrongKey)
	if err := w.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(tr)
	if _, err := re.Execute(context.Background(), realAddr, testSpec()); err == nil {
		t.Fatalf("forged signature must be rejected over messaging")
	}
}

// M3: offline endpoint (topic nobody subscribes to) is silent → ctx timeout.
func TestMessagingOfflineTimesOut(t *testing.T) {
	svc := newTestService(t)
	tr := NewMessagingTransport(svc)
	_, ghost := genWorkerKey(t)

	re := NewRemoteExecutor(tr)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := re.Execute(ctx, ghost, testSpec()); err == nil {
		t.Fatalf("offline endpoint must time out")
	}
}

// M4: full slice-1 scheduler over the real messaging service, quorum mode.
func TestMessagingSchedulerQuorum(t *testing.T) {
	svc := newTestService(t)
	tr := NewMessagingTransport(svc)

	tasks := coprocessor.NewTaskManager(64, time.Hour)
	registry := coprocessor.NewProviderRegistry(1)
	market := coprocessor.NewMarketplace(registry)
	slasher := coprocessor.NewSlashManager(registry, 10)
	challenges := coprocessor.NewChallengeManager(60)

	re := NewRemoteExecutor(tr)
	sched := scheduler.New(scheduler.Config{
		Tasks: tasks, Registry: registry, Market: market,
		Slasher: slasher, Challenges: challenges, Executor: re,
	})
	defer sched.Close()

	outputs := [][]byte{[]byte("agree"), []byte("agree"), []byte("poison")}
	for i := 0; i < 3; i++ {
		key, a := genWorkerKey(t)
		w := NewWorker(key, echoRunner(outputs[i]), tr)
		if err := w.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		defer w.Stop()
		if err := registry.Register(a, 100, []coprocessor.Capability{coprocessor.CapWASM}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		if err := sched.AddWorker(a, scheduler.ClassPhone, fmt.Sprintf("g%d", i)); err != nil {
			t.Fatalf("add worker %d: %v", i, err)
		}
	}

	spec := testSpec()
	spec.Redundancy = 3
	spec.RequireDisperse = true
	spec.MaxPrice = 900
	id, err := sched.Submit(spec, types.Address{0xEE})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := sched.WaitTask(id, 10*time.Second)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Status != coprocessor.TaskVerified || !bytes.Equal(res.Output, []byte("agree")) {
		t.Fatalf("status=%v output=%q (err=%s)", res.Status, res.Output, res.Err)
	}
}

// M5: cross-node path — a second service instance stands in for a remote node
// and forwards to the first via DeliverFromRelay (the relay's job). A worker
// on node B answers a request from an executor on node A.
func TestMessagingCrossNodeRelay(t *testing.T) {
	nodeA := newTestService(t)
	nodeB := newTestService(t)

	// Wire a one-hop "relay": whatever A publishes locally is delivered to B
	// and vice versa. This is exactly the surface Relay uses in production
	// (Publish fans out locally + PublishToNetwork; the remote node calls
	// DeliverFromRelay). We bridge the two in-process to exercise the path
	// without libp2p.
	bridge := func(src, dst *messaging.Service) messagingService {
		return &relayBridge{local: src, remote: dst}
	}
	trA := newMessagingTransportIface(bridge(nodeA, nodeB))
	trB := newMessagingTransportIface(bridge(nodeB, nodeA))

	key, addr := genWorkerKey(t)
	w := NewWorker(key, echoRunner([]byte("remote-node")), trB)
	if err := w.Start(); err != nil {
		t.Fatalf("start worker on B: %v", err)
	}
	defer w.Stop()

	re := NewRemoteExecutor(trA)
	out, err := re.Execute(context.Background(), addr, testSpec())
	if err != nil {
		t.Fatalf("cross-node execute: %v", err)
	}
	if !bytes.Equal(out, []byte("remote-node")) {
		t.Fatalf("output = %q", out)
	}
}

// relayBridge publishes locally (for same-node subscribers) and mirrors to a
// remote node via DeliverFromRelay, standing in for the gossip relay.
type relayBridge struct {
	local  *messaging.Service
	remote *messaging.Service
}

func (b *relayBridge) Publish(topic string, payload []byte, contentType, senderID string) (types.Hash, error) {
	id, err := b.local.Publish(topic, payload, contentType, senderID)
	if err != nil {
		return id, err
	}
	b.remote.DeliverFromRelay(&messaging.Message{
		ID: id, Topic: topic, Payload: payload,
		ContentType: contentType, Timestamp: time.Now(), SenderID: senderID,
	})
	return id, nil
}

func (b *relayBridge) Subscribe(topic string, h messaging.MessageHandler) {
	b.local.Subscribe(topic, h)
}

func (b *relayBridge) Unsubscribe(topic string, h messaging.MessageHandler) {
	b.local.Unsubscribe(topic, h)
}
