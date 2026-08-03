// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// GossipSub subscriber for transaction hash announcements. Filters the batch
// down to hashes this node neither holds nor has already asked for, then
// fetches those bodies from the announcing peer.

package sync

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

const (
	// inFlightCacheSize bounds the record of hashes already requested. At 11k
	// transactions/s this holds roughly the last 6 seconds of announcements,
	// comfortably longer than a fetch takes.
	inFlightCacheSize = 65536

	// inFlightTTL is how long a hash stays suppressed after being requested.
	// Every mesh peer announces the same hash at about the same time, so
	// without this a node opens one fetch per peer for every transaction and
	// re-creates the traffic the announcement was meant to avoid. If the fetch
	// fails, the next announcement after the TTL retries it.
	inFlightTTL = 5 * time.Second

	// maxFetchesPerAnnouncement caps concurrent fetch goroutines started from
	// a single announcement, so a peer cannot make this node spawn unbounded
	// work by announcing a large batch.
	maxFetchesPerAnnouncement = 8
)

// txAnnounceReceived counts announced hashes seen; txAnnounceFetched counts
// bodies actually accepted into the pool. Both are diagnostic: a pipeline that
// announces but never fetches is otherwise invisible, which is exactly how
// every previous transaction-path failure in this codebase hid.
var (
	txAnnounceReceived atomic.Uint64
	txAnnounceFetched  atomic.Uint64
)

type inFlightTracker struct {
	mu    sync.Mutex
	cache *lru.Cache[types.Hash, time.Time]
}

func newInFlightTracker() *inFlightTracker {
	c, err := lru.New[types.Hash, time.Time](inFlightCacheSize)
	if err != nil {
		// Only a non-positive size can fail, and the size is a constant.
		panic(err)
	}
	return &inFlightTracker{cache: c}
}

// claim returns the subset of hashes this node should fetch, marking them as
// in flight. Hashes already in flight within the TTL are dropped.
func (t *inFlightTracker) claim(hashes []types.Hash) []types.Hash {
	now := time.Now()
	out := hashes[:0:0]
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, h := range hashes {
		if at, ok := t.cache.Get(h); ok && now.Sub(at) < inFlightTTL {
			continue
		}
		t.cache.Add(h, now)
		out = append(out, h)
	}
	return out
}

// txHashesSubscriber handles an announcement batch from a peer.
func (s *Service) txHashesSubscriber(ctx context.Context, from peer.ID, data any) error {
	raw, ok := data.(*rawSSZBytes)
	if !ok {
		return nil
	}
	if s.cfg.txPool == nil {
		return nil
	}
	if len(raw.data)%types.HashLength != 0 {
		// Not an error worth penalising a peer over, but it is a protocol
		// violation and silently truncating it would hide a real encoder bug.
		log.Warn("tx announce: payload is not a whole number of hashes", "bytes", len(raw.data), "peer", from.String()[:12])
		return nil
	}
	n := len(raw.data) / types.HashLength
	if n == 0 {
		return nil
	}

	// Ask only for what this node lacks. Under steady-state gossip most of a
	// batch is already known -- every peer announces the same transactions --
	// so this filter, not the fetch, is what keeps the traffic down.
	wanted := make([]types.Hash, 0, n)
	for i := 0; i < n; i++ {
		var h types.Hash
		copy(h[:], raw.data[i*types.HashLength:])
		if s.cfg.txPool.Has(h) {
			continue
		}
		wanted = append(wanted, h)
	}
	if r := txAnnounceReceived.Add(uint64(n)); r <= uint64(n) || r%100000 < uint64(n) {
		log.Info("tx announce: receiving", "announced", r, "fetched", txAnnounceFetched.Load())
	}
	if len(wanted) == 0 {
		return nil
	}
	wanted = s.inFlight.claim(wanted)
	if len(wanted) == 0 {
		return nil
	}

	// One fetch per request-sized slice, bounded. Requesting the whole batch on
	// one stream is the cheap path; the bound only matters for oversized
	// announcements.
	chunks := make([][]types.Hash, 0, maxFetchesPerAnnouncement)
	for len(wanted) > 0 && len(chunks) < maxFetchesPerAnnouncement {
		k := len(wanted)
		if k > maxPooledTxRequest {
			k = maxPooledTxRequest
		}
		chunks = append(chunks, wanted[:k])
		wanted = wanted[k:]
	}
	if len(wanted) > 0 {
		// Say so rather than dropping quietly: a bound that silently discards
		// work reads as "we fetched everything" when it did not.
		log.Warn("tx announce: batch over the per-announcement fetch bound, dropping tail",
			"dropped", len(wanted), "peer", from.String()[:12])
	}

	for _, chunk := range chunks {
		s.wg.Add(1)
		go func(hashes []types.Hash) {
			defer s.wg.Done()
			if got := s.requestPooledTxs(from, hashes); got > 0 {
				txAnnounceFetched.Add(uint64(got))
			}
		}(chunk)
	}
	return nil
}
