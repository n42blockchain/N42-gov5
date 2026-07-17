// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// PacketPublisher is the P2P surface PacketService needs — satisfied by
// p2p.Service (the same shape hotstuff.P2PPublisher uses).
type PacketPublisher interface {
	PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error
	SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error)
}

// PacketService distributes StreamPackets across the IDC fleet
// (design §4, §5a): the leader publishes its block's packet on a gossip
// topic; every node validates and caches inbound packets so any node
// can serve any recent block to mobiles. The packet is self-certifying
// at the cache boundary — its BlockHash must equal the hash of the
// header it carries — so a forged packet cannot occupy a real block's
// cache slot; deeper falsification (wrong read log / txs) is caught by
// the phones' own re-execution against the committed header and can
// only waste, never poison (§8.3).
type PacketService struct {
	cache *PacketCache
	p2p   PacketPublisher
	topic string
	// headNumber bounds unsigned gossip to the local chain neighborhood. A
	// packet carries a self-hashing header, but anyone can manufacture one with
	// MaxUint64 as its number and otherwise evict the rolling cache.
	headNumber func() uint64

	// Swarm distribution (design §5b target form): optional, via SetSeeder.
	seeder Seeder
	seeds  *packetSeeds

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// SetHeadNumber wires the local canonical/applied head used to reject packets
// far outside the node's current chain window. Call before Start.
func (s *PacketService) SetHeadNumber(fn func() uint64) { s.headNumber = fn }

// NewPacketService creates the service. topic is the fully-qualified
// gossip topic (fork-digest formatted, encoding suffix included by the
// subscribe/publish layer conventions of the caller).
func NewPacketService(cache *PacketCache, p2p PacketPublisher, topic string) *PacketService {
	ctx, cancel := context.WithCancel(context.Background())
	return &PacketService{cache: cache, p2p: p2p, topic: topic, ctx: ctx, cancel: cancel}
}

// PublishLocal encodes, caches and gossips a packet this node produced
// (the leader-side path, called from the miner sink). Cache-first so
// this node serves its own block even if the publish fails.
func (s *PacketService) PublishLocal(pkt *evmsdk.StreamPacket, blockNumber uint64) error {
	encoded, err := evmsdk.EncodeStreamPacket(pkt)
	if err != nil {
		return fmt.Errorf("mobileverify: encode packet: %w", err)
	}
	s.cache.Put(pkt.BlockHash, blockNumber, encoded)
	s.seedAsync(pkt.BlockHash, encoded)
	if s.p2p == nil {
		return nil // cache-only mode (no gossip wired)
	}
	if err := s.p2p.PublishToTopic(s.ctx, s.topic, encoded); err != nil {
		return fmt.Errorf("mobileverify: publish packet: %w", err)
	}
	return nil
}

// Start subscribes to the packet topic and caches validated inbound
// packets until Stop.
func (s *PacketService) Start() error {
	if s.p2p == nil {
		return errors.New("mobileverify: no p2p publisher wired")
	}
	sub, err := s.p2p.SubscribeToTopic(s.topic)
	if err != nil {
		return fmt.Errorf("mobileverify: subscribe %s: %w", s.topic, err)
	}
	s.wg.Add(1)
	go s.receiveLoop(sub)
	log.Info("mobileverify: packet service started", "topic", s.topic)
	return nil
}

// Stop terminates the receive loop.
func (s *PacketService) Stop() {
	s.cancel()
	s.wg.Wait()
}

// Get serves an encoded packet from the cache (the HTTP/RPC endpoints
// of phase 3 call this).
func (s *PacketService) Get(blockHash types.Hash) ([]byte, bool) {
	return s.cache.Get(blockHash)
}

func (s *PacketService) receiveLoop(sub *pubsub.Subscription) {
	defer s.wg.Done()
	defer sub.Cancel()
	for {
		msg, err := sub.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			log.Debug("mobileverify: gossip receive error", "err", err)
			continue
		}
		if hash, number, encoded, err := ValidatePacketBytes(msg.Data); err != nil {
			log.Debug("mobileverify: dropping invalid packet", "err", err)
		} else {
			if !s.acceptPeerNumber(number) {
				log.Debug("mobileverify: dropping packet outside local head window", "number", number)
				continue
			}
			if s.cache.PutPeer(hash, number, encoded) {
				s.seedAsync(hash, encoded)
			}
		}
	}
}

func (s *PacketService) acceptPeerNumber(number uint64) bool {
	if s.headNumber == nil {
		return true
	}
	head := s.headNumber()
	// A packet is produced immediately before its candidate block is inserted,
	// so permit a small future skew. Larger gaps must wait until local sync
	// advances the supplied head.
	if number > head && number-head > 2 {
		return false
	}
	if head > s.cache.window && number < head-s.cache.window {
		return false
	}
	return true
}

// ValidatePacketBytes decodes an inbound packet and checks the
// self-certification invariant: the carried header must hash to the
// packet's BlockHash. Returns the identity (hash, number) and the raw
// bytes for caching. Split out for tests and for the phase-3 receipt
// endpoint's reuse.
func ValidatePacketBytes(data []byte) (types.Hash, uint64, []byte, error) {
	pkt, err := evmsdk.DecodeStreamPacket(data)
	if err != nil {
		return types.Hash{}, 0, nil, fmt.Errorf("mobileverify: decode: %w", err)
	}
	hdr := new(block.Header)
	if err := hdr.Unmarshal(pkt.HeaderRLP); err != nil {
		return types.Hash{}, 0, nil, fmt.Errorf("mobileverify: header unmarshal: %w", err)
	}
	if hdr.Hash() != pkt.BlockHash {
		return types.Hash{}, 0, nil, errors.New("mobileverify: packet block hash does not match its header")
	}
	if hdr.Number == nil {
		return types.Hash{}, 0, nil, errors.New("mobileverify: header without number")
	}
	return pkt.BlockHash, hdr.Number.Uint64(), data, nil
}
