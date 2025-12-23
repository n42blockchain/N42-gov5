// Package initialsync includes all initial block download and processing
// logic for the node, using a round robin strategy and a finite-state-machine
// to handle edge-cases in a beacon node's sync status.
package initialsync

import (
	"context"
	"fmt"
	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/internal/p2p"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/paulbellamy/ratecounter"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
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
	// Log throttling
	lastLogTime            time.Time
	lastLogBlock           uint64
	syncStartTime          time.Time
	syncStartBlock         uint64
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
	}

	return s
}

// Start the initial sync service.
func (s *Service) Start() {

	event.GlobalEvent.Send(common.DownloaderStartEvent{})
	defer event.GlobalEvent.Send(common.DownloaderFinishEvent{})

	log.Info("Starting initial chain sync...")
	highestExpectedBlockNr := s.waitForMinimumPeers()
	if err := s.roundRobinSync(highestExpectedBlockNr); err != nil {
		if errors.Is(s.ctx.Err(), context.Canceled) {
			return
		}
		panic(err)
	}
	log.Info(fmt.Sprintf("Synced up to blockNr: %d", s.cfg.Chain.CurrentBlock().Number64().Uint64()))
	s.markSynced()
}

// Stop initial sync.
func (s *Service) Stop() error {
	s.cancel()
	log.Info("InitialSync stopped")
	return nil
}

// Status of initial sync.
func (s *Service) Status() error {
	if s.syncing.Load() == true {
		return errors.New("syncing")
	}
	return nil
}

// Syncing returns true if initial sync is still running.
func (s *Service) Syncing() bool {
	return s.syncing.Load()
}

// Synced returns true if initial sync has been completed.
func (s *Service) Synced() bool {
	return s.synced.Load()
}

// Resync allows a node to start syncing again if it has fallen
// behind the current network head.
func (s *Service) Resync() error {
	// Set it to false since we are syncing again.
	s.markSyncing()
	event.GlobalEvent.Send(common.DownloaderStartEvent{})
	defer func() {
		s.markSynced()
		event.GlobalEvent.Send(common.DownloaderFinishEvent{})
	}() // Reset it at the end of the method.
	//
	beforeBlockNr := s.cfg.Chain.CurrentBlock().Number64()
	highestExpectedBlockNr := s.waitForMinimumPeers()
	if err := s.roundRobinSync(highestExpectedBlockNr); err != nil {
		log.Error("Resync fail", "err", err, "highestExpectedBlockNr", highestExpectedBlockNr, "currentNr", s.cfg.Chain.CurrentBlock().Number64(), "beforeResyncBlockNr", beforeBlockNr)
		return err
	}
	//
	log.Info("Resync attempt complete", "highestExpectedBlockNr", highestExpectedBlockNr, "currentNr", s.cfg.Chain.CurrentBlock().Number64(), "beforeResyncBlockNr", beforeBlockNr)
	return nil
}

// waitForMinimumPeers waits for enough peers to start syncing.
// Returns immediately if:
// 1. MinSyncPeers is set to 0 (standalone/dev mode)
// 2. No bootstrap nodes configured AND current block is genesis (first node in network)
// 3. Enough peers are available
func (s *Service) waitForMinimumPeers() (highestExpectedBlockNr *uint256.Int) {
	required := s.cfg.P2P.GetConfig().MinSyncPeers
	
	// Check if we should skip waiting for peers
	if s.shouldSkipPeerWait() {
		log.Info("Skipping peer wait (genesis node or standalone mode)")
		return s.cfg.Chain.CurrentBlock().Number64()
	}
	
	var peers []peer.ID
	waitCount := 0
	maxWaitCount := 60 // Maximum ~5 minutes wait (60 * 5 seconds)
	
	for {
		highestExpectedBlockNr, peers = s.cfg.P2P.Peers().BestPeers(s.cfg.P2P.GetConfig().MinSyncPeers, s.cfg.Chain.CurrentBlock().Number64())
		if len(peers) >= required {
			break
		}
		
		waitCount++
		log.Info("Waiting for enough suitable peers before syncing (initial-sync.Server)", 
			"suitable", len(peers), 
			"required", required,
			"waitCount", waitCount,
			"maxWait", maxWaitCount)
		
		// After max wait, check if we should proceed anyway
		if waitCount >= maxWaitCount {
			if s.cfg.Chain.CurrentBlock().Number64().IsZero() {
				log.Warn("Timeout waiting for peers on genesis block, proceeding as genesis node")
				return s.cfg.Chain.CurrentBlock().Number64()
			}
			// For non-genesis nodes, continue waiting but log warning
			log.Warn("Extended wait for peers, node may be partitioned from network")
			waitCount = 0 // Reset counter to continue waiting
		}
		
		time.Sleep(handshakePollingInterval)
	}
	return
}

// shouldSkipPeerWait returns true if the node should skip waiting for peers.
// This is the case when:
// 1. MinSyncPeers is 0 (standalone/dev mode)
// 2. No bootstrap nodes are configured AND we're at genesis block (first node in network)
func (s *Service) shouldSkipPeerWait() bool {
	cfg := s.cfg.P2P.GetConfig()
	
	// If MinSyncPeers is 0, always skip (dev/standalone mode)
	if cfg.MinSyncPeers == 0 {
		return true
	}
	
	// Check if we're at genesis block (block 0)
	isGenesis := s.cfg.Chain.CurrentBlock().Number64().IsZero()
	if !isGenesis {
		return false
	}
	
	// Check if no bootstrap nodes are configured
	noBootstrapNodes := len(cfg.BootstrapNodeAddr) == 0 && len(cfg.Discv5BootStrapAddr) == 0
	
	return noBootstrapNodes
}

// markSynced marks node as synced and notifies feed listeners.
func (s *Service) markSyncing() {
	s.syncing.Swap(true)
}

// markSynced marks node as synced and notifies feed listeners.
func (s *Service) markSynced() {
	s.syncing.Swap(false)
	s.synced.Swap(true)
}
