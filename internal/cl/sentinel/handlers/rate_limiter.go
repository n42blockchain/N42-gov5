// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported verbatim from erigon cl/sentinel/handlers (import path rewritten).

//go:build n42el

package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/internal/cl/sentinel/communication"
)

// peerProtocolKey uniquely identifies a (peer, protocol) pair for rate limiting.
type peerProtocolKey struct {
	peerID   string
	protocol string
}

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

func newTokenBucket(maxTokens float64, refillRate float64) *tokenBucket {
	return &tokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// tryConsume attempts to consume n tokens. Returns true if allowed.
func (tb *tokenBucket) tryConsume(n int) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed > 0 {
		tb.tokens += elapsed * tb.refillRate
		if tb.tokens > tb.maxTokens {
			tb.tokens = tb.maxTokens
		}
		tb.lastRefill = now
	}

	cost := float64(n)
	if tb.tokens >= cost {
		tb.tokens -= cost
		return true
	}
	return false
}

// protocolRateConfig defines the rate limit for a protocol.
type protocolRateConfig struct {
	maxTokens  float64 // burst capacity
	refillRate float64 // tokens per second
}

const maxConcurrentRequestsPerPeer = 5
const punishmentDuration = 30 * time.Second
const cleanupInterval = 5 * time.Minute
const bucketIdleExpiry = 10 * time.Minute

// peerRateLimiter enforces per-peer, per-protocol rate limits and per-peer
// concurrent stream caps on incoming ReqResp handlers.
type peerRateLimiter struct {
	protocolLimits map[string]protocolRateConfig
	buckets        sync.Map // map[peerProtocolKey]*tokenBucket
	concurrency    sync.Map // map[string]*atomic.Int32
	punished       sync.Map // map[peerProtocolKey]time.Time
}

func newPeerRateLimiter() *peerRateLimiter {
	rl := &peerRateLimiter{
		protocolLimits: make(map[string]protocolRateConfig),
	}

	// Rate limits aligned with Lighthouse/Lodestar.
	blockRate := protocolRateConfig{maxTokens: 128, refillRate: 12.8}       // 128 per 10s
	blobRate := protocolRateConfig{maxTokens: 72, refillRate: 7.2}          // 72 per 10s
	dataColRate := protocolRateConfig{maxTokens: 16384, refillRate: 1638.4} // 16384 per 10s
	lcRate := protocolRateConfig{maxTokens: 128, refillRate: 12.8}          // 128 per 10s
	pingRate := protocolRateConfig{maxTokens: 2, refillRate: 0.2}           // 2 per 10s
	goodbyeRate := protocolRateConfig{maxTokens: 1, refillRate: 0.1}        // 1 per 10s
	statusRate := protocolRateConfig{maxTokens: 5, refillRate: 0.33}        // 5 per 15s
	metadataRate := protocolRateConfig{maxTokens: 2, refillRate: 0.2}       // 2 per 10s

	rl.protocolLimits[communication.BeaconBlocksByRangeProtocolV2] = blockRate
	rl.protocolLimits[communication.BeaconBlocksByRootProtocolV2] = blockRate
	rl.protocolLimits[communication.BlobSidecarByRangeProtocolV1] = blobRate
	rl.protocolLimits[communication.BlobSidecarByRootProtocolV1] = blobRate
	rl.protocolLimits[communication.DataColumnSidecarsByRangeProtocolV1] = dataColRate
	rl.protocolLimits[communication.DataColumnSidecarsByRootProtocolV1] = dataColRate
	rl.protocolLimits[communication.ExecutionPayloadEnvelopesByRangeProtocolV1] = blockRate
	rl.protocolLimits[communication.ExecutionPayloadEnvelopesByRootProtocolV1] = blockRate
	rl.protocolLimits[communication.PingProtocolV1] = pingRate
	rl.protocolLimits[communication.GoodbyeProtocolV1] = goodbyeRate
	rl.protocolLimits[communication.StatusProtocolV1] = statusRate
	rl.protocolLimits[communication.StatusProtocolV2] = statusRate
	rl.protocolLimits[communication.MetadataProtocolV1] = metadataRate
	rl.protocolLimits[communication.MetadataProtocolV2] = metadataRate
	rl.protocolLimits[communication.MetadataProtocolV3] = metadataRate
	rl.protocolLimits[communication.LightClientOptimisticUpdateProtocolV1] = lcRate
	rl.protocolLimits[communication.LightClientFinalityUpdateProtocolV1] = lcRate
	rl.protocolLimits[communication.LightClientBootstrapProtocolV1] = lcRate
	rl.protocolLimits[communication.LightClientUpdatesByRangeProtocolV1] = lcRate

	return rl
}

// allowRequest checks whether a request from peerID on the given protocol should be allowed.
func (rl *peerRateLimiter) allowRequest(peerID string, protocol string, cost int) bool {
	key := peerProtocolKey{peerID: peerID, protocol: protocol}

	if expiry, ok := rl.punished.Load(key); ok {
		if time.Now().Before(expiry.(time.Time)) {
			return false
		}
		rl.punished.Delete(key)
	}

	return rl.consumeTokens(peerID, protocol, cost)
}

// consumeTokens attempts to consume cost tokens from the bucket for the given peer/protocol.
func (rl *peerRateLimiter) consumeTokens(peerID string, protocol string, cost int) bool {
	if cost <= 0 {
		return true
	}
	key := peerProtocolKey{peerID: peerID, protocol: protocol}
	cfg, hasCfg := rl.protocolLimits[protocol]
	if !hasCfg {
		return true
	}
	bucketI, ok := rl.buckets.Load(key)
	if !ok {
		bucketI, _ = rl.buckets.LoadOrStore(key, newTokenBucket(cfg.maxTokens, cfg.refillRate))
	}
	bucket := bucketI.(*tokenBucket)
	if !bucket.tryConsume(cost) {
		grace := time.Duration(float64(time.Second) / cfg.refillRate)
		bucket.mu.Lock()
		bucket.tokens = 0
		bucket.lastRefill = time.Now().Add(punishmentDuration - grace)
		bucket.mu.Unlock()

		rl.punished.Store(key, time.Now().Add(punishmentDuration))
		return false
	}
	return true
}

// acquireConcurrency tries to increment the in-flight count for a peer.
func (rl *peerRateLimiter) acquireConcurrency(peerID string) bool {
	counterI, _ := rl.concurrency.LoadOrStore(peerID, &atomic.Int32{})
	counter := counterI.(*atomic.Int32)
	for {
		cur := counter.Load()
		if cur >= int32(maxConcurrentRequestsPerPeer) {
			return false
		}
		if counter.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// releaseConcurrency decrements the in-flight count for a peer.
func (rl *peerRateLimiter) releaseConcurrency(peerID string) {
	if counterI, ok := rl.concurrency.Load(peerID); ok {
		counter := counterI.(*atomic.Int32)
		counter.Add(-1)
	}
}

// startCleanup runs a background goroutine that periodically evicts stale entries.
func (rl *peerRateLimiter) startCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.cleanup()
			}
		}
	}()
}

func (rl *peerRateLimiter) cleanup() {
	now := time.Now()

	rl.punished.Range(func(key, value any) bool {
		if now.After(value.(time.Time)) {
			rl.punished.Delete(key)
		}
		return true
	})

	rl.buckets.Range(func(key, value any) bool {
		bucket := value.(*tokenBucket)
		bucket.mu.Lock()
		idle := now.Sub(bucket.lastRefill) > bucketIdleExpiry
		bucket.mu.Unlock()
		if idle {
			rl.buckets.Delete(key)
		}
		return true
	})
}
