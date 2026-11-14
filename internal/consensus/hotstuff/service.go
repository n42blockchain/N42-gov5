// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Top-level HotStuff service: pub/sub wiring and libp2p transport.
// Owns the libp2p pubsub handle, peer identity, crypto primitives and
// the main event loop that fans consensus messages to the engine.
// Serialises output via a sync.Mutex and handles peer connection
// lifecycles so the engine sees a stable validator view.

package hotstuff

import (
	"context"
	"fmt"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// P2PPublisher abstracts the P2P layer for broadcasting and subscribing.
type P2PPublisher interface {
	PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error
	SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error)
	Encoding() encoder.NetworkEncoding
}

// P2PDirectSender extends P2PPublisher with direct peer-to-peer stream messaging.
// Implemented by the full p2p.Service when available.
type P2PDirectSender interface {
	P2PPublisher
	// SendRawBytes sends raw bytes to a specific peer via a libp2p stream protocol.
	SendRawBytes(ctx context.Context, data []byte, topic string, pid peer.ID) error
	// SetStreamHandler registers a handler for inbound stream protocol messages.
	SetStreamHandler(topic string, handler func(data []byte, from peer.ID))
	// ConnectedPeers returns all currently connected peer IDs.
	ConnectedPeers() []peer.ID
}

// BlockProducer abstracts the miner for leader block production.
type BlockProducer interface {
	// TriggerBlockProduction starts building a block for the current view.
	// parentHash, when non-zero, is the block the proposal MUST extend — the
	// HighQC/LockedQC block per HotStuff-2's safety rule. Building on the
	// local head instead makes each leader extend ITS OWN uncommitted
	// same-height candidate, forcing a branch switch on every node each view,
	// oscillating the network so a quorum never forms. Zero = local head
	// (genesis / no lock).
	TriggerBlockProduction(parentHash types.Hash)
	// CommitToCanonical forces the HotStuff-committed block to be the canonical
	// head, so every node's chain follows the single committed chain rather than
	// the candidate each node happened to insert locally.
	CommitToCanonical(hash types.Hash) error
}

// CanonicalCommitterWithTx is the optional extension of BlockProducer that
// exposes the canonicalization transaction, so the consensus state can be
// persisted in the SAME transaction instead of one of its own. Probed by type
// assertion like SpeculativeBlockProducer, so a producer without it simply
// keeps the two-transaction path.
//
// Why it exists: a profile at the 480M tier put mdbx_txn_begin for WRITE
// transactions at 20.4% of all node CPU -- more than the page writes
// themselves. MDBX has one writer and charges txn_begin in proportion to
// freelist pressure, so a second write transaction per committed block costs
// far more than the 415 bytes it carries.
type CanonicalCommitterWithTx interface {
	CommitToCanonicalWith(hash types.Hash, inTx func(kv.RwTx) error) error
}

// SpeculativeBlockProducer is the optional extension of BlockProducer for
// cross-view speculative building: build (but do not seal or propose) a block
// extending parentHash, to be collected by the next TriggerBlockProduction if
// the parent matches. Implemented by the miner; probed by type assertion so
// simpler producers keep working.
type SpeculativeBlockProducer interface {
	PrepareSpeculativeBlock(parentHash types.Hash)
}

// BlockFetcher fetches a proposed block by hash when it isn't already local
// (fetch-on-miss), and catches a lagging/forked node up to the converged chain
// by range (CatchUp). Implemented by the sync layer.
type BlockFetcher interface {
	FetchBlockByHash(hash types.Hash)
	CatchUp()
	// HeightBehind reports how many blocks the local head trails the best peer
	// (0 when synced, ahead, or when no peer height is known yet). Used as the
	// block-production sync-gate: a far-behind validator that produces a block
	// builds on a stale head and self-forks, spawning a rogue candidate that can
	// propagate and destabilize the network.
	HeightBehind() uint64
}

// Service is the integration layer that connects the HotStuff consensus engine
// with the P2P network, persistence, and block production.
//
// It runs four goroutines:
//  1. Output processor — dispatches EngineOutput actions
//  2. Message subscriber — reads gossip messages and feeds to engine
//  3. Pacemaker loop — manages view timeout events
//  4. State persister — periodically saves consensus state to DB
type Service struct {
	voteDB             kv.RwDB
	voteStorePath      string
	voteStoreBinding   []byte
	voteStoreRequested bool
	voteStoreClose     sync.Once
	engine             *HotStuff
	p2p                P2PPublisher
	db                 kv.RwDB
	// journalDB, when set, is a small store of its own that takes the vote
	// journal off the chain database's single writer (vote_journal_db.go).
	journalDB kv.RwDB

	gossipTopic  string        // fully qualified gossip topic string
	rpcTopic     string        // fully qualified RPC topic string
	voteLane     chan struct{} // gossipVoteLane slots (dispatchGossipMessage)
	h2V4Identity *H2V4ChainIdentity

	blockProducer    BlockProducer
	blockFetcher     BlockFetcher
	slashingExecutor *SlashingExecutor

	// Only the output loop accesses pendingProduction. Import goroutines send
	// bounded wakeups after the persisted applied-state check, even when the
	// early execution notification already populated the vote dedup cache.
	pendingProduction *productionRequest
	productionRetry   chan struct{}

	// Rotor single-hop relay for proposal broadcast.
	rotor *Rotor

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Pending block hashes requested by OutputExecuteBlock, waiting for gossip import.
	// Protected by pendingMu (accessed from output goroutine + sync goroutine).
	pendingMu         sync.Mutex
	pendingExecutions map[types.Hash]struct{}

	// notifiedImports dedups EventBlockImported: an uncommitted leader block is
	// re-gossiped once per failing view by every peer, so the same hash arrives
	// tens of times a second. Notifying the engine once per hash (bounded FIFO)
	// stops that storm from spinning the engine mutex and starving the vote path.
	// Protected by pendingMu.
	notifiedImports map[types.Hash]struct{}
	notifiedFIFO    []types.Hash

	// pendingCommits are committed blocks whose CommitToCanonical was deferred
	// because the block hadn't arrived yet. Retried when that block imports —
	// without the retry, nodes that missed the commit-time import never mark
	// the height canonical (observed on-disk: canonical rows missing on 6/7
	// nodes at the first view-changed height). A set, not one slot: a follower
	// that hears several Decides before their bodies must retry every one,
	// not only the last (n42-rs loop149/loop154 hit exactly that with the
	// deferred-execution rule, where the vote no longer waits for the body).
	// Protected by pendingMu.
	pendingCommits map[types.Hash]struct{}
	// recoveredCommit is the persisted committed QC's block when it was
	// above the canonical head at restart (recovered_commit.go): retried on
	// its import notification until it is canonical. Protected by pendingMu.
	recoveredCommit types.Hash
	// deferredProduce remembers a leader view whose gate found the consensus
	// parent not yet applied locally, so the import that lands it can re-run
	// the gate instead of the view waiting out its 6 s timeout. Round 31: at
	// 140k-transaction blocks the QC forms on five imports, and the next
	// leader's own import of that block is often still in flight when its
	// view starts; every such view cost a timeout.
	deferredProduce struct {
		view   ViewNumber
		parent types.Hash
		// waitFor (P2a lag gate): the block whose execution the view waits
		// for, N-k for height N; zero outside lag mode.
		waitFor   types.Hash
		lagParked bool
	}
	// lagGate (P2a): the leader gate under deferred depth k > 1; nil keeps
	// the committed-parent and applied-parent gates.
	lagGate func(parent types.Hash) (bool, types.Hash, error)

	// A CommitQC may arrive before this node executes the committed block. Keep
	// the consecutive failed execution observations so a leader does not extend
	// a persistently unexecutable committed parent. Protected by pendingMu.
	unexecutedCommitHash     types.Hash
	unexecutedCommitFailures uint8

	// availableProbe (P2a) reports blocks held here but not executed yet.
	availableProbe func(types.Hash) bool

	// Epoch schedule for pre-staging future validator sets (loaded from file).
	epochSchedule *EpochSchedule

	// P2P peer refresh callback on epoch transition (injected by node.go).
	peerRefreshFn func()

	// Rate-limit state persistence (persist every N views or on commit).
	lastPersistedView ViewNumber
	persistViewMu     sync.Mutex // lastPersistedView: output loop and commit loop
	// commitCh feeds the commit loop (commit_loop.go).
	commitCh        chan commitJob
	persistInterval uint64
}

// SetAvailabilityProbe attaches the chain's available-block lookup (P2a): a
// block this node holds but has not executed yet is neither fetched again
// on a proposal nor reported as committed-but-unexecuted.
func (s *Service) SetAvailabilityProbe(fn func(types.Hash) bool) {
	s.availableProbe = fn
}

func (s *Service) availableHere(hash types.Hash) bool {
	return s.availableProbe != nil && hash != (types.Hash{}) && s.availableProbe(hash)
}

// NewService creates a new HotStuff service.
func NewService(engine *HotStuff, p2p P2PPublisher, db kv.RwDB, gossipTopic, rpcTopic string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		engine:            engine,
		p2p:               p2p,
		db:                db,
		gossipTopic:       gossipTopic,
		rpcTopic:          rpcTopic,
		voteLane:          make(chan struct{}, gossipVoteLaneMax),
		rotor:             NewRotor(3),
		ctx:               ctx,
		cancel:            cancel,
		persistInterval:   10,
		pendingExecutions: make(map[types.Hash]struct{}),
		notifiedImports:   make(map[types.Hash]struct{}),
		productionRetry:   make(chan struct{}, 1),
	}
}

// SetBlockProducer sets the block producer for leader-driven block production.
func (s *Service) SetBlockProducer(bp BlockProducer) {
	s.blockProducer = bp
}

// SetBlockFetcher sets the fetch-on-miss block fetcher (sync layer).
func (s *Service) SetBlockFetcher(bf BlockFetcher) {
	s.blockFetcher = bf
}

// SetSlashingExecutor injects the equivocation slashing executor.
func (s *Service) SetSlashingExecutor(se *SlashingExecutor) {
	s.slashingExecutor = se
}

// SetEpochSchedule sets the epoch schedule for pre-staging future validator sets.
func (s *Service) SetEpochSchedule(schedule *EpochSchedule) {
	s.epochSchedule = schedule
}

// SetPeerRefreshFn sets the callback invoked on epoch transitions to refresh
// P2P validator peer bindings (Rust: replace_expected_validator_peers_reliable).
func (s *Service) SetPeerRefreshFn(fn func()) {
	s.peerRefreshFn = fn
}

// SetH2V4Identity enables publication of chain-bound Decide proofs for
// cross-client observers. Consensus messages continue on the existing topic.
func (s *Service) SetH2V4Identity(identity H2V4ChainIdentity) {
	s.h2V4Identity = &identity
}

// Start begins the service goroutines.
func (s *Service) Start() error {
	if s.engine.Engine() == nil {
		return fmt.Errorf("hotstuff: consensus engine not initialized, call InitEngine first")
	}
	if err := s.openVoteStore(); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			s.closeVoteStore()
		}
	}()

	// Try to recover persisted state.
	if s.db != nil {
		if err := s.recoverState(); err != nil {
			return fmt.Errorf("recover durable consensus state: %w", err)
		}
	}
	if err := s.seedVoteStore(); err != nil {
		return err
	}

	// A committed QC recovered above the canonical head: its block may be
	// applied already (nothing will notify it again).
	s.retryRecoveredCommit(types.Hash{})

	// A separate vote journal extends the chain database's record: anchor it
	// with one before the first vote (see mergeVoteJournal).
	if err := s.anchorVoteJournal(); err != nil {
		return fmt.Errorf("hotstuff: vote journal: %w", err)
	}

	// Install the durable vote journal before any event can be processed. From
	// here on every vote this node casts is on disk before it goes on the wire.
	if ce := s.engine.Engine(); ce != nil {
		ce.SetVoteJournal(s)
	}

	// Auto-stop pacemaker timer on service shutdown.
	if ce := s.engine.Engine(); ce != nil {
		ce.Pacemaker().WatchContext(s.ctx)
	}

	// Register Rotor relay stream handler for direct validator messaging.
	s.setupRotorStreamHandler()

	s.commitCh = make(chan commitJob, commitQueueLen)
	s.wg.Add(4)
	go s.commitLoop()
	go s.processOutputs()
	go s.pacemakerLoop()
	go s.subscribeMessages()

	log.Info("HotStuff service started",
		"view", s.engine.Engine().CurrentView(),
		"validators", s.engine.Engine().ValidatorCount())
	started = true
	return nil
}

// Stop gracefully shuts down the service.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()

	// Final state persist. NOT on s.ctx — that was just cancelled, and MDBX
	// will not begin a write transaction on a cancelled context.
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.persistStateCtx(ctx)
		cancel()
	}
	s.closeVoteStore()

	log.Info("HotStuff service stopped")
}

func init() {
	envswitch.Declare("N42_PROPOSAL_PUSH_GRACE_MS", envswitch.Config, "Grace before a proposal is pushed, in ms.")
}
