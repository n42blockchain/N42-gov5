// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel. In this caplin revision the gossip-subscribe
// path is intentionally inert here (the SubscribeGossip/Publish methods panic):
// the B+ block-gossip consumer subscribes to beacon_block directly on the
// libp2p pubsub (Sentinel.Host()/p2p.Pubsub()) rather than routing gossip
// through the sentinelproto stream. Kept for type/skeleton compatibility.

//go:build n42el

package sentinel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

type GossipTopic struct {
	Name     string
	CodecStr string
}

type GossipManager struct {
	ch            chan *GossipMessage
	subscriptions sync.Map // map from topic string to *GossipSubscription
}

const maxIncomingGossipMessages = 1 << 16

// construct a new gossip manager that will handle packets with the given handlerfunc
func NewGossipManager(
	ctx context.Context,
) *GossipManager {
	g := &GossipManager{
		ch:            make(chan *GossipMessage, maxIncomingGossipMessages),
		subscriptions: sync.Map{},
	}
	return g
}

func (s *GossipManager) Recv() <-chan *GossipMessage {
	return s.ch
}

func (s *Sentinel) SubscribeGossip(topic GossipTopic, expiration time.Time, opts ...pubsub.TopicOpt) (sub *GossipSubscription, err error) {
	panic("do not call this")
}

func (s *Sentinel) Unsubscribe(topic GossipTopic, opts ...pubsub.TopicOpt) (err error) {
	panic("do not call this")
}

func (g *GossipManager) Close() {
	g.subscriptions.Range(func(key, value any) bool {
		if value != nil {
			value.(*GossipSubscription).Close()
		}
		return true
	})
}

// GossipSubscription abstracts a gossip subscription to write decoded structs.
type GossipSubscription struct {
	gossip_topic GossipTopic
	host         peer.ID
	ch           chan *GossipMessage
	ctx          context.Context
	expiration   atomic.Value // Unix nano for how much we should listen to this topic
	subscribed   atomic.Bool

	topic *pubsub.Topic
	sub   *pubsub.Subscription

	cf context.CancelFunc
	rf pubsub.RelayCancelFunc

	s *Sentinel

	stopCh    chan struct{}
	closeOnce sync.Once
	lock      sync.Mutex
}

func (sub *GossipSubscription) OverwriteSubscriptionExpiry(expiry time.Time) {
	panic("do not call this")
}

// calls the cancel func for the subscriber and closes the topic and sub
func (s *GossipSubscription) Close() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.closeOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
		if s.cf != nil {
			s.cf()
		}
		if s.rf != nil {
			s.rf()
		}
		if s.sub != nil {
			s.sub.Cancel()
			s.sub = nil
		}
		if s.topic != nil {
			s.topic.Close()
			s.topic = nil
		}
	})
}

type GossipMessage struct {
	From      peer.ID
	TopicName string
	Data      []byte
}

func (g *GossipSubscription) Publish(data []byte) error {
	panic("do not call this")
}
