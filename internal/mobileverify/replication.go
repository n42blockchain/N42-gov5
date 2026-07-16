// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"context"
	"errors"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// RegistrationService replicates mobile registrations across the IDC
// fleet (design §3): a new local registration is gossiped as a
// (pubkey, PoP) announcement; every node PoP-verifies it and admits it to
// its pending set, so by the next epoch commit all nodes hold the same
// candidate batch and derive the identical accumulator root. Gossip is
// the mempool; the epoch commit is settlement.
type RegistrationService struct {
	reg   *Registry
	p2p   PacketPublisher // same interface PacketService uses
	topic string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// encodeRegistration is pubkey(48) || pop(96) = 144 bytes.
const registrationLen = 48 + 96

func encodeRegistration(pubkey [48]byte, pop [96]byte) []byte {
	out := make([]byte, 0, registrationLen)
	out = append(out, pubkey[:]...)
	out = append(out, pop[:]...)
	return out
}

func decodeRegistration(b []byte) (pubkey [48]byte, pop [96]byte, err error) {
	if len(b) != registrationLen {
		return pubkey, pop, errors.New("mobileverify: bad registration length")
	}
	copy(pubkey[:], b[:48])
	copy(pop[:], b[48:])
	return pubkey, pop, nil
}

// NewRegistrationService wires gossip replication for the registry.
func NewRegistrationService(reg *Registry, p2p PacketPublisher, topic string) *RegistrationService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &RegistrationService{reg: reg, p2p: p2p, topic: topic, ctx: ctx, cancel: cancel}
	// Every new local registration is announced to the fleet.
	reg.SetOnRegister(func(pubkey [48]byte, pop [96]byte) {
		if s.p2p == nil {
			return
		}
		if err := s.p2p.PublishToTopic(s.ctx, s.topic, encodeRegistration(pubkey, pop)); err != nil {
			log.Debug("mobileverify: registration announce failed", "err", err)
		}
	})
	return s
}

// Start subscribes to the registration topic and adopts inbound
// announcements into the pending set.
func (s *RegistrationService) Start() error {
	if s.p2p == nil {
		return errors.New("mobileverify: no p2p for registration replication")
	}
	sub, err := s.p2p.SubscribeToTopic(s.topic)
	if err != nil {
		return err
	}
	s.wg.Add(1)
	go s.receiveLoop(sub)
	log.Info("mobileverify: registration replication started", "topic", s.topic)
	return nil
}

// Stop ends the receive loop.
func (s *RegistrationService) Stop() {
	s.cancel()
	s.wg.Wait()
}

func (s *RegistrationService) receiveLoop(sub *pubsub.Subscription) {
	defer s.wg.Done()
	defer sub.Cancel()
	for {
		msg, err := sub.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			continue
		}
		pubkey, pop, derr := decodeRegistration(msg.Data)
		if derr != nil {
			continue
		}
		if _, aerr := s.reg.AdoptPending(pubkey, pop); aerr != nil {
			log.Debug("mobileverify: rejected gossiped registration", "err", aerr)
		}
	}
}

// EpochCommitter periodically folds the registry's pending set into the
// committed accumulator and surfaces the new root. The root is the value
// an on-chain batch anchors (design §3, roadmap 6b) — this committer is
// the seam where that anchoring transaction would be submitted; today it
// logs the advance and records it for the status surface.
type EpochCommitter struct {
	reg      *Registry
	interval time.Duration
	onCommit func(root types.Hash, batch int)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	lastRoot types.Hash
	epochs   uint64
}

// NewEpochCommitter commits the registry every interval.
func NewEpochCommitter(reg *Registry, interval time.Duration) *EpochCommitter {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &EpochCommitter{reg: reg, interval: interval, ctx: ctx, cancel: cancel}
}

// SetOnCommit installs a callback fired after each non-empty commit with
// the new root — the on-chain-anchor injection point.
func (c *EpochCommitter) SetOnCommit(fn func(root types.Hash, batch int)) {
	c.mu.Lock()
	c.onCommit = fn
	c.mu.Unlock()
}

// Start runs the commit loop.
func (c *EpochCommitter) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(c.interval)
		defer t.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-t.C:
				c.commitOnce()
			}
		}
	}()
}

func (c *EpochCommitter) commitOnce() {
	root, batch := c.reg.CommitEpoch()
	if batch == 0 {
		return
	}
	c.mu.Lock()
	c.lastRoot = root
	c.epochs++
	hook := c.onCommit
	c.mu.Unlock()
	log.Info("mobileverify: epoch committed", "root", root.Hex()[:12], "batch", batch, "total", c.reg.Count())
	if hook != nil {
		hook(root, batch)
	}
}

// Stop ends the commit loop after a final commit so no pending
// registration is stranded on shutdown.
func (c *EpochCommitter) Stop() {
	c.cancel()
	c.wg.Wait()
	c.commitOnce()
}

// LastRoot returns the most recently committed accumulator root and the
// number of epochs committed (diagnostic / status).
func (c *EpochCommitter) LastRoot() (types.Hash, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRoot, c.epochs
}

// AlarmBuffer is a bounded ring of divergence alarms (design §6): every
// window that closes into more than one receipts-root cohort is an event
// — registered devices re-executed a block to disagreeing roots. Read via
// the status/RPC surface; the on-chain-anchored root makes each alarm a
// verifiable object (roadmap 6b).
type AlarmBuffer struct {
	mu    sync.Mutex
	max   int
	items []DivergenceAlarm
}

// DivergenceAlarm records one divergent window.
type DivergenceAlarm struct {
	BlockHash   types.Hash `json:"blockHash"`
	BlockNumber uint64     `json:"blockNumber"`
	Cohorts     int        `json:"cohorts"`
	AtMs        uint64     `json:"atMs"`
}

// NewAlarmBuffer creates a buffer retaining the most recent max alarms.
func NewAlarmBuffer(max int) *AlarmBuffer {
	if max <= 0 {
		max = 256
	}
	return &AlarmBuffer{max: max}
}

// Record adds one alarm, evicting the oldest past capacity.
func (a *AlarmBuffer) Record(al DivergenceAlarm) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items = append(a.items, al)
	if len(a.items) > a.max {
		a.items = a.items[len(a.items)-a.max:]
	}
}

// Recent returns up to n most-recent alarms, newest first.
func (a *AlarmBuffer) Recent(n int) []DivergenceAlarm {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > len(a.items) {
		n = len(a.items)
	}
	out := make([]DivergenceAlarm, 0, n)
	for i := len(a.items) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, a.items[i])
	}
	return out
}

// Len returns the number of retained alarms.
func (a *AlarmBuffer) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.items)
}
