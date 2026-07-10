// Package initialsync includes all initial block download and processing
// logic for the node, using a round robin strategy and a finite-state-machine
// to handle edge-cases in a beacon node's sync status.
package initialsync

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/paulbellamy/ratecounter"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/internal/p2p"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// Config to set up the initial sync service.
type Config struct {
	P2P   p2p.P2P
	Chain common.IBlockChain
}

// Service service.
type Service struct {
	cfg                    *Config
	ctx                    context.Context
	cancel                 context.CancelFunc
	synced                 atomic.Bool
	syncing                atomic.Bool
	counter                *ratecounter.RateCounter
	highestExpectedBlockNr *uint256.Int
	done                   chan struct{} // closed when Start() returns
	// Log throttling
	lastLogTime    time.Time
	lastLogBlock   uint64
	syncStartTime  time.Time
	syncStartBlock uint64
}

// NewService configures the initial sync service responsible for bringing the node up to the
// latest head of the blockchain.
func NewService(ctx context.Context, cfg *Config) *Service {
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		cfg:     cfg,
		ctx:     ctx,
		cancel:  cancel,
		counter: ratecounter.NewRateCounter(counterSeconds * time.Second),
		done:    make(chan struct{}),
	}
	s.syncing.Store(true)

	return s
}

// Start the initial sync service.
func (s *Service) Start() {
	defer close(s.done)

	event.GlobalEvent.Send(common.DownloaderStartEvent{})
	defer func() {
		s.markSynced()
		event.GlobalEvent.Send(common.DownloaderFinishEvent{})
	}()

	log.Info("Starting chain synchronization...")

	const maxSyncRetries = 10
	syncRetries := 0
	for {
		highestExpectedBlockNr := s.waitForMinimumPeers()
		if err := s.roundRobinSync(highestExpectedBlockNr); err != nil {
			if errors.Is(s.ctx.Err(), context.Canceled) {
				return
			}
			syncRetries++
			if syncRetries > maxSyncRetries {
				log.Error("Sync failed repeatedly, pausing before next attempt", "err", err, "retries", syncRetries)
			} else {
				log.Warn("Sync failed, will retry", "err", err, "attempt", syncRetries)
			}
			// Exponential backoff: 2s, 4s, 8s, ... capped at 60s
			backoff := time.Duration(1<<min(syncRetries, 6)) * time.Second
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			select {
			case <-time.After(backoff):
				continue
			case <-s.ctx.Done():
				return
			}
		} else {
			syncRetries = 0
		}

		currentBlock, err := requireCurrentBlockNumber(s.cfg.Chain, "current block number unavailable")
		if err != nil {
			log.Warn("Stopping sync loop due to unavailable current block number", "err", err)
			break
		}
		newHighest, peers := s.cfg.P2P.Peers().BestPeers(1, currentBlock)
		if len(peers) == 0 || newHighest.Cmp(currentBlock) <= 0 ||
			newHighest.Uint64() <= currentBlock.Uint64()+5 {
			break
		}

		log.Info("Still behind peers after sync, continuing...",
			"currentBlock", currentBlock.Uint64(),
			"peersBlock", newHighest.Uint64())
	}

	log.Info("Chain sync completed", "block", currentBlockNumberOrZero(s.cfg.Chain))
}

// Stop initial sync.
func (s *Service) Stop() error {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		log.Warn("Timed out waiting for initial sync to stop")
	}
	log.Info("InitialSync stopped")
	return nil
}

// Status of initial sync.
func (s *Service) Status() error {
	if s.syncing.Load() {
		return errors.New("syncing")
	}
	return nil
}

// nearSyncedThreshold defines how many blocks before target to start accepting gossip blocks.
// This allows smooth transition from initial-sync to regular sync.
const nearSyncedThreshold = 64

// Syncing returns true if initial sync is still running and not near completion.
// Returns false when sync is complete OR when approaching sync target (within nearSyncedThreshold blocks).
func (s *Service) Syncing() bool {
	return s.syncing.Load() && !s.isNearingSynced()
}

// isNearingSynced returns true if we're within nearSyncedThreshold blocks of the target.
func (s *Service) isNearingSynced() bool {
	if s.highestExpectedBlockNr == nil || s.highestExpectedBlockNr.IsZero() {
		return false
	}
	currentBlock := currentBlockNumberOrZero(s.cfg.Chain)
	targetBlock := s.highestExpectedBlockNr.Uint64()
	if currentBlock >= targetBlock {
		return true
	}
	remaining := targetBlock - currentBlock
	return remaining < nearSyncedThreshold
}

// Synced returns true if initial sync has been completed.
func (s *Service) Synced() bool {
	return s.synced.Load()
}

// Resync allows a node to start syncing again if it has fallen
// behind the current network head.
func (s *Service) Resync() error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	s.markSyncing()
	event.GlobalEvent.Send(common.DownloaderStartEvent{})
	defer func() {
		s.markSynced()
		event.GlobalEvent.Send(common.DownloaderFinishEvent{})
	}()

	beforeBlockNr := currentBlockNumber(s.cfg.Chain)
	highestExpectedBlockNr := s.waitForMinimumPeers()
	if err := s.roundRobinSync(highestExpectedBlockNr); err != nil {
		log.Error("Resync fail", "err", err, "highestExpectedBlockNr", highestExpectedBlockNr, "currentNr", currentBlockNumber(s.cfg.Chain), "beforeResyncBlockNr", beforeBlockNr)
		return err
	}

	log.Info("Resync attempt complete", "highestExpectedBlockNr", highestExpectedBlockNr, "currentNr", currentBlockNumber(s.cfg.Chain), "beforeResyncBlockNr", beforeBlockNr)
	return nil
}

// waitForMinimumPeers waits for enough peers to start syncing.
// Returns immediately if:
// 1. MinSyncPeers is set to 0 (standalone/dev mode)
// 2. No bootstrap nodes configured AND current block is genesis (first node in network)
// 3. Enough peers are available
func (s *Service) waitForMinimumPeers() (highestExpectedBlockNr *uint256.Int) {
	required := s.cfg.P2P.GetConfig().MinSyncPeers

	if s.shouldSkipPeerWait() {
		// Standalone/dev mode (MinSyncPeers==0): don't WAIT for peers. But if peers
		// are already connected and ahead — a static-mesh validator that booted
		// behind the live head — sync UP TO their height. Returning currentBlock
		// made roundRobinSync a no-op (target == head), so a behind node looped
		// "Still behind peers" forever without ever downloading the gap (observed
		// live: node5 stuck at replay head 13013133 while peers were at 13013282).
		cur := currentBlockNumber(s.cfg.Chain)
		if best, peers := s.cfg.P2P.Peers().BestPeers(1, cur); len(peers) > 0 && best != nil && best.Cmp(cur) > 0 {
			log.Info("Standalone mode: peers ahead, syncing up to them",
				"current", cur.Uint64(), "target", best.Uint64())
			return best
		}
		log.Info("Skipping peer discovery (genesis/standalone mode)")
		return cur
	}

	var peers []peer.ID
	waitCount := 0
	maxWaitCount := 60 // Maximum ~5 minutes wait (60 * 5 seconds)

	for {
		highestExpectedBlockNr, peers = s.cfg.P2P.Peers().BestPeers(s.cfg.P2P.GetConfig().MinSyncPeers, currentBlockNumber(s.cfg.Chain))
		if len(peers) >= required {
			log.Info("Found suitable peers for sync", "peers", len(peers), "required", required)
			break
		}

		waitCount++
		// Only log every 5 iterations to reduce spam
		if waitCount == 1 || waitCount%5 == 0 {
			remaining := maxWaitCount - waitCount
			log.Info("Waiting for peers...",
				"found", len(peers),
				"need", required,
				"timeout", fmt.Sprintf("%ds", remaining*5))
		}

		if waitCount >= maxWaitCount {
			if currentBlockNumber(s.cfg.Chain).IsZero() {
				log.Warn("Peer discovery timeout, starting as genesis node")
				return currentBlockNumber(s.cfg.Chain)
			}
			log.Warn("Extended peer wait, node may be isolated from network")
			waitCount = 0
		}

		select {
		case <-time.After(handshakePollingInterval):
		case <-s.ctx.Done():
			return currentBlockNumber(s.cfg.Chain)
		}
	}
	return
}

// shouldSkipPeerWait returns true if the node should skip waiting for peers:
// MinSyncPeers==0 (standalone/dev), or genesis block with no bootstrap/static nodes.
func (s *Service) shouldSkipPeerWait() bool {
	cfg := s.cfg.P2P.GetConfig()
	if cfg.MinSyncPeers == 0 {
		return true
	}
	if !currentBlockNumber(s.cfg.Chain).IsZero() {
		return false
	}
	return len(cfg.BootstrapNodeAddr) == 0 && len(cfg.Discv5BootStrapAddr) == 0 && len(cfg.StaticPeers) == 0
}

// markSyncing marks node as currently syncing.
func (s *Service) markSyncing() {
	s.syncing.Store(true)
}

// markSynced marks node as synced and no longer syncing.
func (s *Service) markSynced() {
	s.syncing.Store(false)
	s.synced.Store(true)
}
