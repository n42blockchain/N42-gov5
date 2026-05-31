// Package serve is the minimal-client-facing read-only stateless serving layer:
// a transport-agnostic Service (headers / blocks / witness / MPT anchors / code)
// hardened against abuse with per-request caps and a per-IP bandwidth limiter,
// composed with the existing per-IP request-rate limiter
// (modules/rpc/jsonrpc.RateLimiter). See docs/ethel/idc-stateless-node-architecture.md.
package serve

import (
	"sync"
	"time"
)

// ByteLimiter is a per-IP byte (bandwidth) token bucket. The request-COUNT
// limiter (jsonrpc.RateLimiter) bounds how OFTEN a peer calls; this bounds how
// many BYTES it can pull/sec, so a few large responses can't saturate egress.
// The clock is injectable for deterministic tests.
type ByteLimiter struct {
	mu     sync.Mutex
	rate   float64 // refill bytes/sec
	burst  float64 // max accumulated bytes
	maxIPs int
	now    func() time.Time
	ips    map[string]*byteState
}

type byteState struct {
	tokens float64
	last   time.Time
	seen   time.Time
}

// NewByteLimiter caps each IP to bytesPerSec sustained with a burst ceiling,
// tracking at most maxIPs distinct IPs (least-recently-seen evicted on overflow).
func NewByteLimiter(bytesPerSec, burst float64, maxIPs int) *ByteLimiter {
	if maxIPs <= 0 {
		maxIPs = 65536
	}
	return &ByteLimiter{
		rate:   bytesPerSec,
		burst:  burst,
		maxIPs: maxIPs,
		now:    time.Now,
		ips:    make(map[string]*byteState),
	}
}

// SetClock overrides the clock (tests).
func (l *ByteLimiter) SetClock(now func() time.Time) { l.now = now }

// Allow charges `cost` bytes to ip's bucket. Returns true (and consumes) when the
// bucket has at least `cost` tokens; false otherwise (caller responds 429). A
// cost larger than burst is always rejected (oversized response — should be
// blocked by the request cap first).
func (l *ByteLimiter) Allow(ip string, cost int) bool {
	if cost < 0 {
		cost = 0
	}
	if float64(cost) > l.burst {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	st, ok := l.ips[ip]
	if !ok {
		if len(l.ips) >= l.maxIPs {
			l.evictOldestLocked()
		}
		st = &byteState{tokens: l.burst, last: now}
		l.ips[ip] = st
	} else {
		elapsed := now.Sub(st.last).Seconds()
		if elapsed > 0 {
			st.tokens += elapsed * l.rate
			if st.tokens > l.burst {
				st.tokens = l.burst
			}
			st.last = now
		}
	}
	st.seen = now
	if st.tokens >= float64(cost) {
		st.tokens -= float64(cost)
		return true
	}
	return false
}

// evictOldestLocked drops the least-recently-seen IP. Caller holds l.mu.
func (l *ByteLimiter) evictOldestLocked() {
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, st := range l.ips {
		if first || st.seen.Before(oldest) {
			oldestIP, oldest, first = ip, st.seen, false
		}
	}
	if !first {
		delete(l.ips, oldestIP)
	}
}

// Tracked returns how many IPs are currently tracked (observability/tests).
func (l *ByteLimiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips)
}
