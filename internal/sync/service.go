// Package sync includes all chain-synchronization logic for the N42 node,
// including gossip-sub blocks, txs, and other p2p messages, as well as
// the ability to process and respond to block requests by peers.
package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/utils"
)

const (
	rangeLimit       = 1024
	seenBlockSize    = 1000
	badBlockSize     = 1000
	maxRequestBlocks = 1024

	syncMetricsInterval          = 10 * time.Second
	maintainPeerStatusesInterval = 2 * time.Minute
	resyncInterval               = 1 * time.Minute

	enableFullSSZDataLogging = false

	// ttfbTimeout is the maximum time to wait for the first byte of a request response.
	ttfbTimeout = 10 * time.Second
	// respTimeout is the maximum time for complete response transfer.
	respTimeout = 20 * time.Second
)

var (
	errWrongMessage = errors.New("wrong pubsub message")
	errNilMessage   = errors.New("nil pubsub message")

	newBadBlockCache = func() (*lru.Cache[types.Hash, bool], error) {
		return lru.New[types.Hash, bool](badBlockSize)
	}
	newSeenBlockCache = func() (*lru.Cache[types.Hash, *block.Block], error) {
		return lru.New[types.Hash, *block.Block](seenBlockSize)
	}
)

// validationFn is a functional type for p2p validation options.
type validationFn func(ctx context.Context) (pubsub.ValidationResult, error)

// config holds dependencies for the sync service.
type config struct {
	p2p           p2p.P2P
	chain         common.IBlockChain
	initialSync   Checker
	earliestBlock func() uint64 // returns earliest available block, 0 = all available
}

// Service is responsible for handling all runtime p2p related operations as the
// main entry point for network messages.
type Service struct {
	cfg    *config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks background goroutines (RunEveryWithWG)

	subHandler  *subTopicHandler
	rateLimiter *limiter

	seenBlockCache *lru.Cache[types.Hash, *block.Block]
	seenBlockLock  sync.RWMutex
	badBlockLock   sync.RWMutex
	badBlockCache  *lru.Cache[types.Hash, bool]

	validateBlockLock sync.RWMutex
}

// NewService initializes new regular sync service.
func NewService(ctx context.Context, opts ...Option) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)
	r := &Service{
		ctx:    ctx,
		cancel: cancel,
		cfg:    &config{},
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			cancel()
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	r.subHandler = newSubTopicHandler()
	r.rateLimiter = newRateLimiter(r.cfg.p2p)
	if err := r.initCaches(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize sync caches: %w", err)
	}

	r.registerRPCHandlers()

	digest, err := r.currentForkDigest()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to retrieve current fork digest: %w", err)
	}
	r.registerSubscribers(digest)
	//go r.forkWatcher()

	return r, nil
}

// Start the regular sync service.
func (s *Service) Start() {
	s.cfg.p2p.AddConnectionHandler(s.reValidatePeer, s.sendGoodbye)
	s.cfg.p2p.AddDisconnectionHandler(func(_ context.Context, p peer.ID) error {
		// Rate limit reconnection attempts for disconnected peers
		if nextValidTime, err := s.cfg.p2p.Peers().NextValidTime(p); err == nil && time.Now().After(nextValidTime) {
			s.cfg.p2p.Peers().SetNextValidTime(p, time.Now().Add(10*time.Minute))
		}
		return nil
	})
	s.cfg.p2p.AddPingMethod(s.sendPingRequest)
	s.maintainPeerStatuses()
	s.resyncIfBehind()

	// Update sync metrics.
	s.wg.Add(1)
	utils.RunEveryWithWG(s.ctx, syncMetricsInterval, s.updateMetrics, &s.wg)
}

// Stop the regular sync service.
func (s *Service) Stop() error {
	// Cancel context first so all RunEveryWithWG goroutines begin exiting.
	s.cancel()
	// Wait for all background goroutines to finish.
	s.wg.Wait()

	// Removing RPC Stream handlers.
	for _, p := range s.cfg.p2p.Host().Mux().Protocols() {
		s.cfg.p2p.Host().RemoveStreamHandler(protocol.ID(p))
	}
	// Deregister Topic Subscribers.
	for _, t := range s.cfg.p2p.PubSub().GetTopics() {
		s.unSubscribeFromTopic(t)
	}
	if s.rateLimiter != nil {
		s.rateLimiter.free()
	}
	return nil
}

// Status of the currently running regular sync service.
func (s *Service) Status() error {
	// If our HighestBlockNumber lower than our peers are reporting then we might be out of sync.
	currentBlock, err := requireCurrentBlockNumber(s.cfg.chain, "current block number unavailable")
	if err != nil {
		return err
	}
	if currentBlock.Uint64()+1 < s.cfg.p2p.Peers().HighestBlockNumber().Uint64() {
		return errors.New("out of sync")
	}
	return nil
}

// initCaches initializes LRU caches used to deduplicate incoming blocks
// and track bad blocks to prevent spam amplification.
func (s *Service) initCaches() error {
	var err error
	s.badBlockCache, err = newBadBlockCache()
	if err != nil {
		return fmt.Errorf("create bad block cache: %w", err)
	}
	s.seenBlockCache, err = newSeenBlockCache()
	if err != nil {
		return fmt.Errorf("create seen block cache: %w", err)
	}
	return nil
}

// SetEarliestBlock sets the function that returns the earliest available block
// number. P2P range requests for blocks before this number are rejected.
// This must be called before Start.
func (s *Service) SetEarliestBlock(fn func() uint64) {
	s.cfg.earliestBlock = fn
}

// Checker defines an interface for verifying whether a node is currently
// synchronizing its chain with the rest of the peers in the network.
type Checker interface {
	Syncing() bool
	Synced() bool
	Status() error
	Resync() error
}
