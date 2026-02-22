package sync

import (
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// subTopicHandler provides thread-safe CRUD operations on the
// subscription topic map and tracks per-digest subscription counts.
type subTopicHandler struct {
	sync.RWMutex
	subTopics map[string]*pubsub.Subscription
	digestMap map[[4]byte]int
}

func newSubTopicHandler() *subTopicHandler {
	return &subTopicHandler{
		subTopics: make(map[string]*pubsub.Subscription),
		digestMap: make(map[[4]byte]int),
	}
}

func (s *subTopicHandler) addTopic(topic string, sub *pubsub.Subscription) {
	s.Lock()
	defer s.Unlock()
	s.subTopics[topic] = sub
	digest, err := p2p.ExtractGossipDigest(topic)
	if err != nil {
		log.Error("Could not retrieve digest", "err", err)
		return
	}
	s.digestMap[digest]++
}

func (s *subTopicHandler) topicExists(topic string) bool {
	s.RLock()
	defer s.RUnlock()
	_, ok := s.subTopics[topic]
	return ok
}

func (s *subTopicHandler) removeTopic(topic string) {
	s.Lock()
	defer s.Unlock()
	delete(s.subTopics, topic)
	digest, err := p2p.ExtractGossipDigest(topic)
	if err != nil {
		log.Error("Could not retrieve digest", "err", err)
		return
	}
	// Defensive: ensure digest count is valid before decrementing.
	if count, ok := s.digestMap[digest]; !ok || count <= 0 {
		delete(s.digestMap, digest)
		return
	}
	s.digestMap[digest]--
	if s.digestMap[digest] == 0 {
		delete(s.digestMap, digest)
	}
}

func (s *subTopicHandler) digestExists(digest [4]byte) bool {
	s.RLock()
	defer s.RUnlock()
	count, ok := s.digestMap[digest]
	return ok && count > 0
}

func (s *subTopicHandler) allTopics() []string {
	s.RLock()
	defer s.RUnlock()
	topics := make([]string, 0, len(s.subTopics))
	for t := range s.subTopics {
		topics = append(topics, t)
	}
	return topics
}

func (s *subTopicHandler) subForTopic(topic string) *pubsub.Subscription {
	s.RLock()
	defer s.RUnlock()
	return s.subTopics[topic]
}
