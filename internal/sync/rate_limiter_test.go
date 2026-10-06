package sync

import (
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

type rateLimiterP2PStub struct {
	p2p.P2P
	cfg *conf.P2PConfig
}

func (s *rateLimiterP2PStub) GetConfig() *conf.P2PConfig { return s.cfg }
func (s *rateLimiterP2PStub) Encoding() encoder.NetworkEncoding {
	return encoder.SszNetworkEncoder{}
}

func TestNewRateLimiterDefaults(t *testing.T) {
	p2pStub := &rateLimiterP2PStub{cfg: &conf.P2PConfig{}}
	l := newRateLimiter(p2pStub)
	if l == nil {
		t.Fatalf("expected non-nil limiter")
	}
	if _, err := l.topicCollector(rpcLimiterTopic); err != nil {
		t.Fatalf("expected rpc limiter topic collector to exist: %v", err)
	}
}

func TestLimiterTopicCollectorMissing(t *testing.T) {
	p2pStub := &rateLimiterP2PStub{cfg: &conf.P2PConfig{}}
	l := newRateLimiter(p2pStub)
	if _, err := l.topicCollector("does-not-exist"); err == nil {
		t.Fatalf("expected error for missing topic collector")
	}
}

func TestLimiterFree(t *testing.T) {
	p2pStub := &rateLimiterP2PStub{cfg: &conf.P2PConfig{}}
	l := newRateLimiter(p2pStub)
	l.free()
	if len(l.limiterMap) != 0 {
		t.Fatalf("expected limiterMap cleared after free, got %d entries", len(l.limiterMap))
	}
}

func TestRetrieveCollectorRequiresLock(t *testing.T) {
	p2pStub := &rateLimiterP2PStub{cfg: &conf.P2PConfig{}}
	l := newRateLimiter(p2pStub)
	// Calling retrieveCollector without holding the lock must error.
	if _, err := l.retrieveCollector(rpcLimiterTopic); err == nil {
		t.Fatalf("expected error when lock is not held")
	}
}
