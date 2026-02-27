package initialsync

import (
	"context"
	"math"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
)

// peerLock returns peer lock for a given peer. If lock is not found, it is created.
func (f *blocksFetcher) peerLock(pid peer.ID) *peerLock {
	f.Lock()
	defer f.Unlock()
	if lock, ok := f.peerLocks[pid]; ok && lock != nil {
		lock.accessed = time.Now()
		return lock
	}
	f.peerLocks[pid] = &peerLock{
		accessed: time.Now(),
	}
	return f.peerLocks[pid]
}

// removeStalePeerLocks is a cleanup procedure which removes stale locks.
func (f *blocksFetcher) removeStalePeerLocks(age time.Duration) {
	f.Lock()
	defer f.Unlock()
	for peerID, lock := range f.peerLocks {
		if time.Since(lock.accessed) >= age {
			lock.Lock()
			delete(f.peerLocks, peerID)
			lock.Unlock()
		}
	}
}

// selectFailOverPeer randomly selects fail over peer from the list of available peers.
func (f *blocksFetcher) selectFailOverPeer(excludedPID peer.ID, peers []peer.ID) (peer.ID, error) {
	if len(peers) == 0 {
		return "", errNoPeersAvailable
	}

	availablePeers := make([]peer.ID, 0, len(peers))
	for _, p := range peers {
		if p != excludedPID {
			availablePeers = append(availablePeers, p)
		}
	}
	if len(availablePeers) == 0 {
		return "", errNoPeersAvailable
	}
	ind := f.rand.Int() % len(availablePeers)
	return availablePeers[ind], nil
}

// waitForMinimumPeers spins and waits up until enough peers are available.
// Returns immediately if:
// 1. MinSyncPeers is set to 0 (standalone/dev mode)
// 2. No bootstrap nodes configured AND current block is genesis (first node in network)
// 3. Enough peers are available
func (f *blocksFetcher) waitForMinimumPeers(ctx context.Context) ([]peer.ID, error) {
	cfg := f.p2p.GetConfig()
	required := cfg.MinSyncPeers

	if f.shouldSkipPeerWait() {
		log.Info("Skipping peer wait in blocksFetcher (genesis node or standalone mode)")
		return nil, nil
	}

	waitCount := 0
	maxWaitCount := 60 // Maximum ~5 minutes wait (60 * 5 seconds)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_, peers := f.p2p.Peers().BestPeers(cfg.MinSyncPeers, f.chain.CurrentBlock().Number64())
		if len(peers) >= required {
			return peers, nil
		}

		waitCount++
		log.Info("Waiting for enough suitable peers before syncing (blocksFetcher)",
			"suitable", len(peers),
			"required", required,
			"waitCount", waitCount)

		if waitCount >= maxWaitCount {
			if f.chain.CurrentBlock().Number64().IsZero() {
				log.Warn("Timeout waiting for peers on genesis block (blocksFetcher), proceeding")
				return nil, nil
			}
			// Reset counter for non-genesis nodes
			log.Warn("Extended wait for peers in blocksFetcher, node may be partitioned from network")
			waitCount = 0
		}

		select {
		case <-time.After(handshakePollingInterval):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// shouldSkipPeerWait returns true if the fetcher should skip waiting for peers.
func (f *blocksFetcher) shouldSkipPeerWait() bool {
	cfg := f.p2p.GetConfig()
	if cfg.MinSyncPeers == 0 {
		return true
	}
	if !f.chain.CurrentBlock().Number64().IsZero() {
		return false
	}
	return len(cfg.BootstrapNodeAddr) == 0 && len(cfg.Discv5BootStrapAddr) == 0
}

// filterPeers returns transformed list of peers, weight sorted by scores and capacity remaining.
// List can be further constrained using peersPercentage, where only percentage of peers are returned.
func (f *blocksFetcher) filterPeers(ctx context.Context, peers []peer.ID, peersPercentage float64) []peer.ID {
	ctx, span := trace.StartSpan(ctx, "initialsync.filterPeers")
	defer span.End()

	if len(peers) == 0 {
		return peers
	}

	// Sort peers using both block provider score and, custom, capacity based score (see
	// peerFilterCapacityWeight if you want to give different weights to provider's and capacity
	// scores).
	// Scores produced are used as weights, so peers are ordered probabilistically i.e. peer with
	// a higher score has higher chance to end up higher in the list.
	scorer := f.p2p.Peers().Scorers().BlockProviderScorer()
	peers = scorer.WeightSorted(f.rand, peers, func(peerID peer.ID, blockProviderScore float64) float64 {
		remaining, capacity := float64(f.rateLimiter.Remaining(peerID.String())), float64(f.rateLimiter.Capacity())
		// When capacity is close to exhaustion, allow less performant peer to take a chance.
		// Otherwise, there's a good chance system will be forced to wait for rate limiter.
		if remaining < float64(f.blocksPerPeriod) {
			return 0.0
		}
		capScore := remaining / capacity
		overallScore := blockProviderScore*(1.0-f.capacityWeight) + capScore*f.capacityWeight
		return math.Round(overallScore*scorers.ScoreRoundingFactor) / scorers.ScoreRoundingFactor
	})

	return trimPeers(peers, peersPercentage, f.p2p.GetConfig().MinSyncPeers)
}

// trimPeers limits peer list, returning only specified percentage of peers.
func trimPeers(peers []peer.ID, peersPercentage float64, minSyncPeers int) []peer.ID {
	if len(peers) == 0 {
		return peers
	}
	limit := math.Round(float64(len(peers)) * peersPercentage)
	limit = math.Max(limit, float64(minSyncPeers))
	limit = math.Min(limit, float64(len(peers)))

	limitInt := int(math.Floor(limit))
	limitInt = max(limitInt, 0)
	limitInt = min(limitInt, len(peers))
	return peers[:limitInt]
}
