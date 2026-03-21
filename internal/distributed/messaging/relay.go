// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package messaging

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

const (
	// DefaultMessageShards is the default number of message topic shards.
	DefaultMessageShards = 8

	// defaultSeenCacheSize is the default deduplication cache size.
	defaultSeenCacheSize = 65536

	// MessageTopicFormat is the GossipSub topic for messaging shards.
	MessageTopicFormat = "/n42/msg/shard/%d"
)

// P2PPublisher is the interface the relay needs from the P2P layer.
type P2PPublisher interface {
	PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error
	SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error)
}

// Relay bridges the messaging Service with the libp2p GossipSub network.
type Relay struct {
	cfg     *conf.MessagingCfg
	service *Service
	p2p     P2PPublisher
	key     *ecdsa.PrivateKey

	// Deduplication: ring buffer seen cache
	seenMu    sync.Mutex
	seenCache map[types.Hash]struct{}
	seenRing  []types.Hash
	seenIdx   int
	seenMax   int

	subs   []*pubsub.Subscription
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRelay creates a new message relay.
func NewRelay(cfg *conf.MessagingCfg, service *Service, p2p P2PPublisher, key *ecdsa.PrivateKey) *Relay {
	ctx, cancel := context.WithCancel(context.Background())
	seenMax := cfg.DeduplicateCacheSize
	if seenMax <= 0 {
		seenMax = defaultSeenCacheSize
	}
	return &Relay{
		cfg:       cfg,
		service:   service,
		p2p:       p2p,
		key:       key,
		seenCache: make(map[types.Hash]struct{}, seenMax),
		seenRing:  make([]types.Hash, 0, seenMax),
		seenIdx:   0,
		seenMax:   seenMax,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start subscribes to all message shard topics and begins forwarding.
func (r *Relay) Start() error {
	shards := r.cfg.MessageShards
	if shards <= 0 {
		shards = DefaultMessageShards
	}

	for i := 0; i < shards; i++ {
		topic := fmt.Sprintf(MessageTopicFormat, i)
		sub, err := r.p2p.SubscribeToTopic(topic)
		if err != nil {
			log.Error("Failed to subscribe to message shard", "shard", i, "err", err)
			continue
		}
		r.subs = append(r.subs, sub)
		r.wg.Add(1)
		go r.readLoop(sub, i)
	}

	log.Info("Message relay started", "shards", shards)
	return nil
}

// Stop cancels all relay goroutines.
func (r *Relay) Stop() {
	r.cancel()
	for _, sub := range r.subs {
		sub.Cancel()
	}
	r.wg.Wait()
	log.Info("Message relay stopped")
}

// PublishToNetwork wraps a message in an Envelope and broadcasts it via GossipSub.
func (r *Relay) PublishToNetwork(msg *Message) error {
	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     msg.Topic,
		Payload:   msg.Payload,
		Timestamp: msg.Timestamp.UnixNano(),
		Nonce:     uint64(time.Now().UnixNano()),
	}

	if r.key != nil {
		if err := SignEnvelope(env, r.key); err != nil {
			return fmt.Errorf("sign envelope: %w", err)
		}
	}

	id := EnvelopeID(env)
	r.markSeen(id)

	data, err := EncodeEnvelope(env)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}

	shard := r.topicShard(msg.Topic)
	topic := fmt.Sprintf(MessageTopicFormat, shard)

	if err := r.p2p.PublishToTopic(r.ctx, topic, data); err != nil {
		return fmt.Errorf("publish to gossipsub: %w", err)
	}

	return nil
}

// readLoop reads messages from a GossipSub subscription.
func (r *Relay) readLoop(sub *pubsub.Subscription, shard int) {
	defer r.wg.Done()
	for {
		msg, err := sub.Next(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			log.Debug("Error reading from message shard", "shard", shard, "err", err)
			continue
		}
		r.handleIncoming(msg.Data)
	}
}

// handleIncoming processes an incoming GossipSub message.
func (r *Relay) handleIncoming(data []byte) {
	env, err := DecodeEnvelope(data)
	if err != nil {
		log.Debug("Failed to decode message envelope", "err", err)
		return
	}

	// Dedup check
	id := EnvelopeID(env)
	if r.isSeen(id) {
		return
	}
	r.markSeen(id)

	// Validate envelope
	maxSize := r.cfg.MaxEnvelopeSize
	if maxSize <= 0 {
		maxSize = MaxEnvelopeSize
	}
	if err := ValidateEnvelope(env, maxSize); err != nil {
		log.Debug("Invalid message envelope", "err", err)
		return
	}

	// Convert to internal Message and deliver to service
	msg := &Message{
		ID:          id,
		Topic:       env.Topic,
		Payload:     env.Payload,
		ContentType: "application/octet-stream",
		Timestamp:   time.Unix(0, env.Timestamp),
		SenderID:    fmt.Sprintf("%x", env.Sender),
	}

	r.service.DeliverFromRelay(msg)
}

// topicShard deterministically assigns a topic to a shard.
func (r *Relay) topicShard(topic string) int {
	shards := r.cfg.MessageShards
	if shards <= 0 {
		shards = DefaultMessageShards
	}
	// Simple hash-based sharding
	var hash uint32
	for _, b := range []byte(topic) {
		hash = hash*31 + uint32(b)
	}
	return int(hash % uint32(shards))
}

// markSeen adds an ID to the seen cache with ring buffer eviction.
func (r *Relay) markSeen(id types.Hash) {
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	if _, ok := r.seenCache[id]; ok {
		return
	}
	if len(r.seenRing) < r.seenMax {
		r.seenRing = append(r.seenRing, id)
	} else {
		// Evict oldest entry at current ring index
		oldest := r.seenRing[r.seenIdx]
		delete(r.seenCache, oldest)
		r.seenRing[r.seenIdx] = id
	}
	r.seenIdx = (r.seenIdx + 1) % r.seenMax
	r.seenCache[id] = struct{}{}
}

// isSeen checks if an ID is in the seen cache.
func (r *Relay) isSeen(id types.Hash) bool {
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	_, ok := r.seenCache[id]
	return ok
}
