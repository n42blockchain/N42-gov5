// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MessagingTransport adapts the address-routed worker.Transport onto the
// topic-based distributed messaging service (gossip relay + local dispatch),
// so the compute scheduler and its remote workers run across real nodes
// without either side knowing about topics.
//
// Routing: each endpoint address maps to a dedicated topic
// (topicPrefix + addr.hex). Register subscribes the endpoint to its own
// topic; Send publishes to the destination's topic. In-process delivery goes
// through Service.Publish's local handler dispatch; cross-node delivery
// arrives via the relay and Service.DeliverFromRelay, which fans out to the
// same local topic handlers — so one adapter works for both single-node
// many-workers and multi-node fleets.
//
// The sender address rides in the message's SenderID (also the rate-limit
// key). Payloads are the opaque transport bytes; the compute protocol's own
// secp256k1 signatures (protocol.go) — not the messaging layer — provide
// authenticity, so a forged SenderID cannot forge a result.

package worker

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/messaging"
)

// messagingService is the subset of *messaging.Service the adapter needs
// (keeps the adapter testable and the dependency explicit).
type messagingService interface {
	Publish(topic string, payload []byte, contentType, senderID string) (types.Hash, error)
	Subscribe(topic string, handler messaging.MessageHandler)
	Unsubscribe(topic string, handler messaging.MessageHandler)
}

const (
	// computeTopicPrefix namespaces compute-transport topics. It sits under
	// the messaging /n42/msg/ subscribe-filter prefix via the relay's topic
	// mapping; the adapter only needs the logical string here.
	computeTopicPrefix = "compute/endpoint/"
	computeContentType = "application/x-n42-compute"
)

// MessagingTransport implements Transport over a messaging.Service.
type MessagingTransport struct {
	svc      messagingService
	handlers map[types.Address]messaging.MessageHandler // for Unsubscribe by pointer
}

// NewMessagingTransport wraps a messaging service.
func NewMessagingTransport(svc *messaging.Service) *MessagingTransport {
	return &MessagingTransport{
		svc:      svc,
		handlers: make(map[types.Address]messaging.MessageHandler),
	}
}

// newMessagingTransportIface is the interface-typed constructor used by tests.
func newMessagingTransportIface(svc messagingService) *MessagingTransport {
	return &MessagingTransport{svc: svc, handlers: make(map[types.Address]messaging.MessageHandler)}
}

func endpointTopic(addr types.Address) string {
	return computeTopicPrefix + addr.Hex()
}

// Register implements Transport. The messaging handler unwraps the sender
// address from SenderID and invokes h.
func (m *MessagingTransport) Register(addr types.Address, h Handler) error {
	if _, dup := m.handlers[addr]; dup {
		return fmt.Errorf("worker: messaging endpoint %s already registered", addr.Hex())
	}
	mh := func(msg *messaging.Message) {
		from := types.HexToAddress(msg.SenderID)
		h(from, msg.Payload)
	}
	m.handlers[addr] = mh
	m.svc.Subscribe(endpointTopic(addr), mh)
	return nil
}

// Unregister implements Transport.
func (m *MessagingTransport) Unregister(addr types.Address) {
	mh, ok := m.handlers[addr]
	if !ok {
		return
	}
	m.svc.Unsubscribe(endpointTopic(addr), mh)
	delete(m.handlers, addr)
}

// Send implements Transport: publish payload to the destination's topic.
// A publish error (oversize payload, rate limit) is surfaced; an offline
// receiver — a topic no node subscribes to — is silent, matching the
// Transport contract that silence, not error, means "receiver absent".
func (m *MessagingTransport) Send(from, to types.Address, payload []byte) error {
	_, err := m.svc.Publish(endpointTopic(to), payload, computeContentType, from.Hex())
	return err
}
