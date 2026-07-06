// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Transport abstracts the wire between the scheduler-side RemoteExecutor and
// workers. Slice 2 ships LocalBus (in-memory, for tests and single-process
// deployments); the production adapter wraps the distributed messaging
// service (gossip topics + E2E envelopes) behind this same interface.

package worker

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Handler receives one inbound payload addressed to a registered endpoint.
type Handler func(from types.Address, payload []byte)

// Transport delivers opaque payloads between addressed endpoints. Delivery is
// asynchronous and unreliable-by-contract: senders must handle silence with
// their own deadlines (the scheduler's lease does exactly that).
type Transport interface {
	// Register binds addr to a handler. One handler per address.
	Register(addr types.Address, h Handler) error
	// Unregister removes the binding.
	Unregister(addr types.Address)
	// Send delivers payload to the handler registered at `to`. Payloads to
	// unregistered addresses are dropped silently (an offline worker looks
	// like silence, never like an error).
	Send(from, to types.Address, payload []byte) error
}

// LocalBus is an in-memory Transport. Each Send delivers on a fresh
// goroutine so no handler can deadlock a sender.
type LocalBus struct {
	mu       sync.RWMutex
	handlers map[types.Address]Handler
}

// NewLocalBus creates an empty bus.
func NewLocalBus() *LocalBus {
	return &LocalBus{handlers: make(map[types.Address]Handler)}
}

// Register implements Transport.
func (b *LocalBus) Register(addr types.Address, h Handler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.handlers[addr]; dup {
		return fmt.Errorf("worker: transport address %s already registered", addr.Hex())
	}
	b.handlers[addr] = h
	return nil
}

// Unregister implements Transport.
func (b *LocalBus) Unregister(addr types.Address) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers, addr)
}

// Send implements Transport.
func (b *LocalBus) Send(from, to types.Address, payload []byte) error {
	b.mu.RLock()
	h, ok := b.handlers[to]
	b.mu.RUnlock()
	if !ok {
		return nil // offline receiver = silence, by contract
	}
	cp := append([]byte(nil), payload...)
	go h(from, cp)
	return nil
}
