package sync

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/pkg/errors"
	"github.com/trailofbits/go-mutexasserts"

	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/leakybucket"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/log"
)

const defaultBurstLimit = 5

const leakyBucketPeriod = 1 * time.Second

// rpcLimiterTopic is a synthetic topic used to validate all incoming RPC requests.
const rpcLimiterTopic = "rpc-limiter-topic"

type limiter struct {
	limiterMap map[string]*leakybucket.Collector
	p2p        p2p.P2P
	sync.RWMutex
}

// newRateLimiter creates a multi-RPC protocol rate limiter with separate
// collectors for each topic.
func newRateLimiter(p2pProvider p2p.P2P) *limiter {
	addEncoding := func(topic string) string {
		return topic + p2pProvider.Encoding().ProtocolSuffix()
	}

	// Block rate limits with defaults for nil P2PLimit.
	var (
		allowedBlocksPerSecond float64 = 500
		allowedBlocksBurst     int64   = 1000
		blockLimiterPeriod             = 1 * time.Second
	)

	if cfg := p2pProvider.GetConfig(); cfg != nil && cfg.P2PLimit != nil {
		allowedBlocksPerSecond = float64(cfg.P2PLimit.BlockBatchLimit)
		allowedBlocksBurst = int64(cfg.P2PLimit.BlockBatchLimitBurstFactor * cfg.P2PLimit.BlockBatchLimit)
		blockLimiterPeriod = time.Duration(cfg.P2PLimit.BlockBatchLimiterPeriod) * time.Second
	}

	newDefaultCollector := func(rate float64, burst int64, period time.Duration) *leakybucket.Collector {
		return leakybucket.NewCollector(rate, burst, period, false)
	}

	topicMap := make(map[string]*leakybucket.Collector, len(p2p.RPCTopicMappings))
	topicMap[addEncoding(p2p.RPCGoodByeTopicV1)] = newDefaultCollector(1, 1, leakyBucketPeriod)
	topicMap[addEncoding(p2p.RPCPingTopicV1)] = newDefaultCollector(1, defaultBurstLimit, leakyBucketPeriod)
	topicMap[addEncoding(p2p.RPCStatusTopicV1)] = newDefaultCollector(1, defaultBurstLimit, leakyBucketPeriod)
	topicMap[addEncoding(p2p.RPCBodiesDataTopicV1)] = newDefaultCollector(allowedBlocksPerSecond, allowedBlocksBurst, blockLimiterPeriod)
	topicMap[addEncoding(p2p.RPCHeadersDataTopicV1)] = newDefaultCollector(allowedBlocksPerSecond, allowedBlocksBurst, blockLimiterPeriod)
	topicMap[rpcLimiterTopic] = newDefaultCollector(5, defaultBurstLimit*2, leakyBucketPeriod)

	return &limiter{limiterMap: topicMap, p2p: p2pProvider}
}

// topicCollector returns the collector for the provided topic.
func (l *limiter) topicCollector(topic string) (*leakybucket.Collector, error) {
	l.RLock()
	defer l.RUnlock()
	return l.retrieveCollector(topic)
}

// validateRequest checks whether a request with the given cost is within rate limits.
func (l *limiter) validateRequest(stream network.Stream, amt uint64) error {
	l.RLock()
	defer l.RUnlock()

	topic := string(stream.Protocol())
	collector, err := l.retrieveCollector(topic)
	if err != nil {
		return err
	}

	key := stream.Conn().RemotePeer().String()
	remaining := collector.Remaining(key)

	// Treat each request as a minimum of 1.
	if amt == 0 {
		amt = 1
	}
	if amt > uint64(remaining) {
		log.Warn("validate Request failure",
			"key", key,
			"topic", topic,
			"count", amt,
			"remaining", remaining,
		)
		l.rejectStream(stream)
		return p2ptypes.ErrRateLimited
	}
	return nil
}

// validateRawRpcRequest validates all incoming RPC streams from external peers
// against the general RPC rate limit.
func (l *limiter) validateRawRpcRequest(stream network.Stream) error {
	l.RLock()
	defer l.RUnlock()

	collector, err := l.retrieveCollector(rpcLimiterTopic)
	if err != nil {
		return err
	}

	key := stream.Conn().RemotePeer().String()
	if collector.Remaining(key) < 1 {
		l.rejectStream(stream)
		return p2ptypes.ErrRateLimited
	}
	return nil
}

// rejectStream increments the bad response score for the peer and writes an
// error response. Must be called while holding at least an RLock.
func (l *limiter) rejectStream(stream network.Stream) {
	l.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
	writeErrorResponseToStream(responseCodeInvalidRequest, p2ptypes.ErrRateLimited.Error(), stream, l.p2p)
}

// add records the cost for the stream's topic in the leaky bucket.
func (l *limiter) add(stream network.Stream, amt int64) {
	l.Lock()
	defer l.Unlock()

	topic := string(stream.Protocol())
	collector, err := l.retrieveCollector(topic)
	if err != nil {
		log.Error("collector does not exist", "topic", topic, "err", err)
		return
	}
	collector.Add(stream.Conn().RemotePeer().String(), amt)
}

// addRawStream records a single request cost for the peer against the general RPC limiter.
func (l *limiter) addRawStream(stream network.Stream) {
	l.Lock()
	defer l.Unlock()

	collector, err := l.retrieveCollector(rpcLimiterTopic)
	if err != nil {
		log.Error("collector does not exist", "topic", rpcLimiterTopic, "err", err)
		return
	}
	collector.Add(stream.Conn().RemotePeer().String(), 1)
}

// free releases all collectors and clears the limiter map.
func (l *limiter) free() {
	l.Lock()
	defer l.Unlock()

	// Deduplicate collectors since multiple topics may share the same instance.
	freed := make(map[*leakybucket.Collector]struct{})
	for _, collector := range l.limiterMap {
		if _, done := freed[collector]; !done {
			collector.Free()
			freed[collector] = struct{}{}
		}
	}
	clear(l.limiterMap)
}

// retrieveCollector returns the collector for the given topic. The caller must
// hold either a read lock or write lock on the limiter.
func (l *limiter) retrieveCollector(topic string) (*leakybucket.Collector, error) {
	if !mutexasserts.RWMutexLocked(&l.RWMutex) && !mutexasserts.RWMutexRLocked(&l.RWMutex) {
		return nil, errors.New("limiter.retrieveCollector: caller must hold read/write lock")
	}
	collector, ok := l.limiterMap[topic]
	if !ok {
		return nil, errors.Errorf("collector does not exist for topic %s", topic)
	}
	return collector, nil
}
