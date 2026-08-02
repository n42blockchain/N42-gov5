package p2p

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/peers/peerdata"
)

const (
	// Overlay parameters.
	gossipSubD   = 8
	gossipSubDlo = 6

	// Gossip parameters.
	gossipSubMcacheLen    = 6
	gossipSubMcacheGossip = 3

	gossipSubHeartbeatInterval = 700 * time.Millisecond

	digestLength      = 4
	gossipTopicPrefix = "/n42/"

	defaultPeerWaitTimeout = 30 * time.Second
)

var errInvalidTopic = errors.New("invalid topic format")

// GossipMaxSize is the message-size cap handed to the pubsub router. It tracks
// the encoder's gossip bound — including N42_MAX_GOSSIP_MB — instead of being
// pinned at 1 MiB. Pinned, it silently capped the network at the default no
// matter what the knob said: block producers would size blocks to the raised
// budget, the encoder would accept them, and libp2p would then refuse to
// publish, leaving the chain on whatever direct-push path remained.
func GossipMaxSize() uint64 { return encoder.MaxGossipWireSize() }

// JoinTopic joins a PubSub topic, returning the existing handle if already joined.
func (s *Service) JoinTopic(topic string, opts ...pubsub.TopicOpt) (*pubsub.Topic, error) {
	s.joinedTopicsLock.Lock()
	defer s.joinedTopicsLock.Unlock()

	if _, ok := s.joinedTopics[topic]; !ok {
		topicHandle, err := s.pubsub.Join(topic, opts...)
		if err != nil {
			return nil, err
		}
		s.joinedTopics[topic] = topicHandle
	}

	return s.joinedTopics[topic], nil
}

// LeaveTopic closes a topic and removes it from the joined topics map.
// Returns an error if there are outstanding event handlers or subscriptions.
func (s *Service) LeaveTopic(topic string) error {
	s.joinedTopicsLock.Lock()
	defer s.joinedTopicsLock.Unlock()

	if t, ok := s.joinedTopics[topic]; ok {
		if err := t.Close(); err != nil {
			return err
		}
		delete(s.joinedTopics, topic)
	}
	return nil
}

// PublishToTopic joins (if necessary) and publishes a message to a PubSub topic.
// It waits up to defaultPeerWaitTimeout for at least one peer to be available.
func (s *Service) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	topicHandle, err := s.JoinTopic(topic)
	if err != nil {
		return err
	}

	timeout := time.After(defaultPeerWaitTimeout)
	for {
		if len(topicHandle.ListPeers()) > 0 || s.cfg.MinSyncPeers == 0 {
			return topicHandle.Publish(ctx, data, opts...)
		}
		select {
		case <-ctx.Done():
			return errors.Wrapf(ctx.Err(), "unable to find requisite number of peers for topic %s, 0 peers found to publish to", topic)
		case <-timeout:
			return errors.Errorf("timeout waiting for peers for topic %s after %v", topic, defaultPeerWaitTimeout)
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// SubscribeToTopic joins (if necessary) and subscribes to a PubSub topic,
// applying scoring parameters.
func (s *Service) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	topicHandle, err := s.JoinTopic(topic)
	if err != nil {
		return nil, err
	}
	scoringParams, err := s.topicScoreParams(topic)
	if err != nil {
		return nil, err
	}
	if err := topicHandle.SetScoreParams(scoringParams); err != nil {
		return nil, err
	}
	logGossipParameters(topic, scoringParams)
	return topicHandle.Subscribe(opts...)
}

// peerInspector scrapes peer scoring data and feeds it to the gossip scorer.
func (s *Service) peerInspector(peerMap map[peer.ID]*pubsub.PeerScoreSnapshot) {
	for pid, snap := range peerMap {
		s.peers.Scorers().GossipScorer().SetGossipData(pid, snap.Score,
			snap.BehaviourPenalty, convertTopicScores(snap.Topics))
	}
}

// pubsubOptions returns the PubSub configuration options for the GossipSub router.
func (s *Service) pubsubOptions() []pubsub.Option {
	return []pubsub.Option{
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithNoAuthor(),
		pubsub.WithMessageIdFn(func(pmsg *pubsubpb.Message) string {
			return MsgID(s.genesisHash, pmsg)
		}),
		pubsub.WithSubscriptionFilter(s),
		pubsub.WithPeerOutboundQueueSize(pubsubQueueSize),
		pubsub.WithMaxMessageSize(int(GossipMaxSize())),
		pubsub.WithValidateQueueSize(pubsubQueueSize),
		pubsub.WithPeerScore(peerScoringParams()),
		pubsub.WithPeerScoreInspect(s.peerInspector, time.Minute),
		pubsub.WithGossipSubParams(pubsubGossipParam()),
		pubsub.WithRawTracer(gossipTracer{host: s.host}),
	}
}

func pubsubGossipParam() pubsub.GossipSubParams {
	p := pubsub.DefaultGossipSubParams()
	p.D = gossipSubD
	p.Dlo = gossipSubDlo
	p.HeartbeatInterval = gossipSubHeartbeatInterval
	p.HistoryLength = gossipSubMcacheLen
	p.HistoryGossip = gossipSubMcacheGossip
	return p
}

func setPubSubParameters() {
	pubsub.TimeCacheDuration = 550 * gossipSubHeartbeatInterval
}

// convertTopicScores converts libp2p topic score snapshots to the local form.
func convertTopicScores(topicMap map[string]*pubsub.TopicScoreSnapshot) map[string]*peerdata.TopicScoreSnapshot {
	newMap := make(map[string]*peerdata.TopicScoreSnapshot, len(topicMap))
	for t, s := range topicMap {
		if s == nil {
			continue
		}
		newMap[t] = &peerdata.TopicScoreSnapshot{
			TimeInMesh:               uint64(s.TimeInMesh.Milliseconds()),
			FirstMessageDeliveries:   float32(s.FirstMessageDeliveries),
			MeshMessageDeliveries:    float32(s.MeshMessageDeliveries),
			InvalidMessageDeliveries: float32(s.InvalidMessageDeliveries),
		}
	}
	return newMap
}

// ExtractGossipDigest extracts the fork digest from a gossip topic string.
// Topics follow the format /n42/{fork-digest}/{topic} and this method
// extracts the fork digest as a 4-byte array.
func ExtractGossipDigest(topic string) ([4]byte, error) {
	if len(topic) < len(gossipTopicPrefix)+1 || topic[:len(gossipTopicPrefix)] != gossipTopicPrefix {
		return [4]byte{}, errInvalidTopic
	}
	start := len(gossipTopicPrefix)
	end := strings.Index(topic[start:], "/")
	if end == -1 {
		return [4]byte{}, errInvalidTopic
	}
	end += start

	digest, err := hex.DecodeString(topic[start:end])
	if err != nil {
		return [4]byte{}, err
	}
	if len(digest) != digestLength {
		return [4]byte{}, errors.Errorf("invalid digest length wanted %d but got %d", digestLength, len(digest))
	}
	return utils.ToBytes4(digest), nil
}
