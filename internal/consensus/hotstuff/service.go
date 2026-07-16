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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	vm "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
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

// blockProductionSyncGate is the height lag (in blocks) above which a leader
// pauses block production and catches up first. A small allowance absorbs the
// transient one-block lag of normal operation (a leader that just committed a
// height its peers already have) without halting production, while still
// catching the startup self-fork case where a node trails by many blocks.
const blockProductionSyncGate = 2

// committedUnexecutedRetryLimit is the number of consecutive local execution
// checks that may fail for the same committed hash before leaders refuse to
// extend it. A short grace period covers normal block-arrival skew.
const committedUnexecutedRetryLimit = 3

// heightBehind returns how far the local head trails the network, or 0 when no
// block fetcher is wired (preserving unconditional production for chains without
// the sync layer).
func (s *Service) heightBehind() uint64 {
	if s.blockFetcher == nil {
		return 0
	}
	return s.blockFetcher.HeightBehind()
}

// blockExecutionStatus checks the applied-lineage evidence exposed by the sync
// layer. The bool result is false only when the probe is unavailable; a missing
// local header is a checked, unexecuted result.
func (s *Service) blockExecutionStatus(hash types.Hash) (applied, checked bool, number uint64) {
	probe, ok := s.blockFetcher.(interface {
		BlockApplied(types.Hash, uint64) bool
	})
	if !ok {
		return false, false, 0
	}
	if s.db == nil {
		return false, true, 0
	}
	haveHeader := false
	_ = s.db.View(s.ctx, func(tx kv.Tx) error {
		if hdr, err := rawdb.ReadHeaderByHash(tx, hash); err == nil && hdr != nil && hdr.Number != nil {
			number = hdr.Number.Uint64()
			haveHeader = true
		}
		return nil
	})
	if !haveHeader {
		return false, true, 0
	}
	return probe.BlockApplied(hash, number), true, number
}

// observeCommittedExecution records high-visibility evidence that a committed
// block has not executed locally. countMetric is true only for the CommitQC
// event; retry observations update the consecutive-failure guard without
// inflating the event counter.
func (s *Service) observeCommittedExecution(hash types.Hash, countMetric bool) (bool, uint8) {
	applied, checked, number := s.blockExecutionStatus(hash)
	if !checked {
		return true, 0 // compatibility for deployments without the applied probe
	}
	s.pendingMu.Lock()
	if applied {
		s.unexecutedCommitHash = types.Hash{}
		s.unexecutedCommitFailures = 0
		s.pendingMu.Unlock()
		return true, 0
	}
	if s.unexecutedCommitHash != hash {
		s.unexecutedCommitHash = hash
		s.unexecutedCommitFailures = 1
	} else if s.unexecutedCommitFailures < ^uint8(0) {
		s.unexecutedCommitFailures++
	}
	failures := s.unexecutedCommitFailures
	s.pendingMu.Unlock()

	if countMetric {
		metricCommittedUnexecuted.Inc()
	}
	log.Error("hotstuff: committed block not executed locally",
		"hash", hash, "number", number, "failures", failures)
	return false, failures
}

// committedParentBlocked rechecks a tracked committed parent and fails closed
// after repeated local execution failures. Other parents are unaffected.
func (s *Service) committedParentBlocked(parentHash types.Hash) bool {
	s.pendingMu.Lock()
	tracked := s.unexecutedCommitHash == parentHash && parentHash != (types.Hash{})
	s.pendingMu.Unlock()
	if !tracked {
		return false
	}
	applied, failures := s.observeCommittedExecution(parentHash, false)
	if applied || failures < committedUnexecutedRetryLimit {
		return false
	}
	log.Error("hotstuff: refusing block production on unexecuted committed parent",
		"hash", parentHash, "failures", failures)
	return true
}

func (s *Service) triggerBlockProduction(view ViewNumber, parentHash types.Hash) {
	if behind := s.heightBehind(); behind > blockProductionSyncGate {
		log.Warn("hotstuff: behind peers, skipping block production and catching up",
			"view", view, "behind", behind, "gate", blockProductionSyncGate)
		if s.blockFetcher != nil {
			go s.blockFetcher.CatchUp()
		}
		return
	}
	if s.committedParentBlocked(parentHash) {
		return
	}
	s.blockProducer.TriggerBlockProduction(parentHash)
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
	engine *HotStuff
	p2p    P2PPublisher
	db     kv.RwDB

	gossipTopic string // fully qualified gossip topic string
	rpcTopic    string // fully qualified RPC topic string

	blockProducer    BlockProducer
	blockFetcher     BlockFetcher
	slashingExecutor *SlashingExecutor

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

	// pendingCommit is a committed block whose CommitToCanonical was deferred
	// because the block hadn't arrived yet. Retried when that block imports —
	// without the retry, nodes that missed the commit-time import never mark
	// the height canonical (observed on-disk: canonical rows missing on 6/7
	// nodes at the first view-changed height). Protected by pendingMu.
	pendingCommit types.Hash

	// A CommitQC may arrive before this node executes the committed block. Keep
	// the consecutive failed execution observations so a leader does not extend
	// a persistently unexecutable committed parent. Protected by pendingMu.
	unexecutedCommitHash     types.Hash
	unexecutedCommitFailures uint8

	// Epoch schedule for pre-staging future validator sets (loaded from file).
	epochSchedule *EpochSchedule

	// P2P peer refresh callback on epoch transition (injected by node.go).
	peerRefreshFn func()

	// Rate-limit state persistence (persist every N views or on commit).
	lastPersistedView ViewNumber
	persistInterval   uint64
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
		rotor:             NewRotor(3),
		ctx:               ctx,
		cancel:            cancel,
		persistInterval:   10,
		pendingExecutions: make(map[types.Hash]struct{}),
		notifiedImports:   make(map[types.Hash]struct{}),
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

// Start begins the service goroutines.
func (s *Service) Start() error {
	if s.engine.Engine() == nil {
		return fmt.Errorf("hotstuff: consensus engine not initialized, call InitEngine first")
	}

	// Try to recover persisted state.
	if s.db != nil {
		if err := s.recoverState(); err != nil {
			log.Warn("hotstuff: failed to recover persisted state", "err", err)
		}
	}

	// Auto-stop pacemaker timer on service shutdown.
	if ce := s.engine.Engine(); ce != nil {
		ce.Pacemaker().WatchContext(s.ctx)
	}

	// Register Rotor relay stream handler for direct validator messaging.
	s.setupRotorStreamHandler()

	s.wg.Add(3)
	go s.processOutputs()
	go s.pacemakerLoop()
	go s.subscribeMessages()

	log.Info("HotStuff service started",
		"view", s.engine.Engine().CurrentView(),
		"validators", s.engine.Engine().ValidatorCount())
	return nil
}

// Stop gracefully shuts down the service.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()

	// Final state persist.
	if s.db != nil {
		s.persistState()
	}

	log.Info("HotStuff service stopped")
}

// processOutputs reads EngineOutput actions and dispatches them.
func (s *Service) processOutputs() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case output := <-s.engine.OutputCh():
			s.handleOutput(output)
		}
	}
}

func (s *Service) handleOutput(output EngineOutput) {
	switch output.Type {
	case OutputBroadcast:
		// Off the serial output loop: handleOutput also runs heavyweight work
		// inline (CommitToCanonical unwind+re-execute, persistState MDBX
		// commits), and PublishToTopic can spin up to 30s waiting for topic
		// peers on a degraded mesh. A vote/timeout broadcast queued behind
		// either misses its view window and the round times out — liveness
		// death spiral. Gossip publish is concurrency-safe, and every message
		// carries its view, so cross-message ordering is not load-bearing
		// (the proposal's block-data pre-broadcast stays ordered inside the
		// same goroutine).
		go func(out EngineOutput) {
			// Leader: broadcast block data via gossip BEFORE sending Proposal,
			// so followers can import the block and vote on it.
			if out.Message != nil && out.Message.Type == MsgProposal {
				s.broadcastBlockData(out.Hash)
			}
			s.handleBroadcast(out)
		}(output)
	case OutputSendToValidator:
		go s.handleSendToValidator(output)
	case OutputExecuteBlock:
		// A Proposal references this block. When it is imported (via direct push
		// or fetch), NotifyBlockImported fires EventBlockImported and the engine
		// casts its import-gated vote.
		log.Debug("hotstuff: execute block requested", "hash", output.Hash)
		s.pendingMu.Lock()
		s.pendingExecutions[output.Hash] = struct{}{}
		s.pendingMu.Unlock()
		// Fetch-on-miss: if we don't already have the proposed block (its direct
		// push was dropped), fetch it by hash so we can import and vote. Without
		// this, a single dropped push stalls the view to the full timeout and the
		// chain head never advances. FetchBlockByHash is a no-op if we have it.
		if s.blockFetcher != nil {
			s.blockFetcher.FetchBlockByHash(output.Hash)
		}
	case OutputBlockCommitted:
		log.Info("hotstuff: block committed", "view", output.View, "hash", output.Hash)
		s.observeCommittedExecution(output.Hash, true)
		// Make the committed block the canonical head so every node's chain follows
		// the single committed chain (the miner may have locally inserted a
		// different same-height candidate via resultLoop). Skipped silently if the
		// block isn't in the DB yet — it reconciles on a later commit / future-block
		// import once the block arrives via direct push.
		if s.blockProducer != nil {
			if err := s.blockProducer.CommitToCanonical(output.Hash); err != nil {
				log.Debug("hotstuff: commit-to-canonical deferred", "hash", output.Hash, "err", err)
				// Remember it — NotifyBlockImported retries when the block lands.
				s.pendingMu.Lock()
				s.pendingCommit = output.Hash
				s.pendingMu.Unlock()
			} else {
				s.pendingMu.Lock()
				s.pendingCommit = types.Hash{}
				s.pendingMu.Unlock()
			}
		}
		updateMetricsBlockCommitted(output.View)
		// Mark pending reconfigurations as committed now that the block has a CommitQC.
		if rm := s.engine.Engine().ReconfigManager(); rm != nil && rm.HasPendingChanges() {
			rm.MarkCommitted()
		}
		s.persistState()

		// Process pending equivocation slashing.
		if s.slashingExecutor != nil {
			if n, err := s.slashingExecutor.ProcessPendingSlashing(s.ctx); err != nil {
				log.Error("hotstuff: slashing execution failed", "err", err)
			} else if n > 0 {
				log.Warn("hotstuff: validators slashed for equivocation", "count", n)
			}
		}

		// Derive block randomness from CommitQC aggregate signature.
		// The CommitQC requires 2f+1 signers, making the aggregate signature
		// unpredictable by any single validator (threshold VUF).
		if output.QC != nil && len(output.QC.AggregateSignature) > 0 {
			// randomness = keccak256(aggregateSignature || blockNumber_LE_8bytes)
			// Use explicit allocation to avoid corrupting the QC's signature slice.
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], uint64(output.View))
			sigLen := len(output.QC.AggregateSignature)
			randomInput := make([]byte, sigLen+8)
			copy(randomInput, output.QC.AggregateSignature)
			copy(randomInput[sigLen:], buf[:])
			randomness := crypto.Keccak256Hash(randomInput)
			vm.SetBlockRandomness(uint64(output.View), randomness)
		}
	case OutputViewChanged:
		log.Debug("hotstuff: view changed", "view", output.View)
		updateMetricsViewChanged(output.View)
		// Clear stale pending executions from previous view.
		s.pendingMu.Lock()
		for k := range s.pendingExecutions {
			delete(s.pendingExecutions, k)
		}
		s.pendingMu.Unlock()
		// Trigger block production immediately if we are the new leader. NO delay:
		// views can rotate faster than the old FastPropose delay (~200ms), and any
		// delay lets the view advance past us — after which IsCurrentLeader is false
		// and production is skipped, so the miner never produces and HotStuff spins
		// on non-miner proposals while the chain head never advances. Direct block
		// push (resultLoop) makes the old gossip-warmup delay unnecessary.
		isLeader := s.engine.Engine().IsCurrentLeader()
		log.Info("hotstuff: view changed", "view", output.View, "isLeader", isLeader, "hasProducer", s.blockProducer != nil)
		if isLeader && s.blockProducer != nil {
			// Sync-gate: a validator whose local head trails the network must NOT
			// produce a block. It would build on a stale head and self-fork —
			// spawning a rogue candidate that, once its view is committed
			// elsewhere, propagates and destabilizes the whole network (observed
			// live: a reseeded node produced its own block one above its stale
			// head the instant it started, before view/range sync caught up).
			// Catch up first; a later view we lead will find us current.
			// Extend the LockedQC (HighQC) block — HotStuff-2's safety rule. A
			// leader that extends its own local head instead proposes on top of
			// an UNCOMMITTED same-height candidate, and the network oscillates
			// between sibling branches without ever forming a quorum.
			lq := s.engine.Engine().LockedQC()
			s.triggerBlockProduction(output.View, lq.BlockHash)
		}
		// Rate-limited persistence.
		if output.View-s.lastPersistedView >= s.persistInterval {
			s.persistState()
		}
	case OutputEpochStaged:
		// Persist staged validator set for crash recovery (fixes Property 7b).
		log.Info("hotstuff: epoch staged, persisting for crash recovery", "epoch", output.NewEpoch, "validators", output.ValidatorCount)
		if s.db != nil {
			if ce := s.engine.Engine(); ce != nil {
				if epoch, validators, f, ok := ce.StagedEpochInfoSafe(); ok {
					if err := s.db.Update(s.ctx, func(tx kv.RwTx) error {
						return SaveStagedEpoch(tx, epoch, validators, f)
					}); err != nil {
						log.Error("failed to persist staged epoch", "err", err)
					}
				}
			}
		}
	case OutputSyncRequired:
		log.Warn("hotstuff: sync required", "localView", output.LocalView, "targetView", output.TargetView)
		// We are behind (a Decide arrived far ahead of our view). Pull the
		// converged chain by range and reorg onto it. Run async so the event
		// loop isn't blocked by InsertChain; CatchUp self-guards against
		// concurrent runs.
		if s.blockFetcher != nil {
			go s.blockFetcher.CatchUp()
		}
	case OutputEquivocationDetected:
		log.Warn("hotstuff: EQUIVOCATION detected",
			"view", output.View, "validator", output.Validator,
			"hash1", output.Hash1, "hash2", output.Hash2)
		updateMetricsEquivocation()
		// Persist evidence for future slashing.
		if s.db != nil {
			if err := s.db.Update(s.ctx, func(tx kv.RwTx) error {
				return SaveEquivocationEvidence(tx, output.View, output.Validator, output.Hash1, output.Hash2)
			}); err != nil {
				log.Error("failed to persist equivocation evidence", "err", err)
			}
		}
	case OutputEpochTransition:
		log.Info("hotstuff: epoch transition", "epoch", output.NewEpoch, "validators", output.ValidatorCount)

		// Persist staged epoch state for crash recovery.
		if s.db != nil {
			if err := s.db.Update(s.ctx, func(tx kv.RwTx) error {
				return ClearStagedEpoch(tx) // staged → active, clear persisted staged
			}); err != nil {
				log.Error("failed to clear staged epoch", "err", err)
			}
		}

		// Pre-stage next epoch from schedule (Rust: pre-stage N+1 after activating N).
		if s.epochSchedule != nil {
			if ce := s.engine.Engine(); ce != nil {
				ce.PreStageFromScheduleSafe(s.epochSchedule)
			}
		}

		// Notify P2P layer to refresh validator peer bindings.
		if s.peerRefreshFn != nil {
			s.peerRefreshFn()
		}
	}
}

func (s *Service) handleBroadcast(output EngineOutput) {
	if output.Message == nil || s.p2p == nil {
		return
	}

	data, err := EncodeConsensusMsg(output.Message)
	if err != nil {
		log.Error("hotstuff: failed to encode broadcast message", "err", err)
		return
	}

	// Compress with snappy for gossip.
	var buf bytes.Buffer
	enc := s.p2p.Encoding()
	if _, err := enc.EncodeGossip(&buf, &rawSSZMarshaler{data: data}); err != nil {
		log.Error("hotstuff: failed to encode gossip", "err", err)
		return
	}

	gossipBytes := buf.Bytes()
	topic := s.gossipTopic + enc.ProtocolSuffix()

	log.Info("hotstuff: broadcasting consensus message", "type", output.Message.Type, "topic", topic, "bytes", len(gossipBytes))

	// Use Rotor single-hop relay for proposal broadcasts.
	if output.Message.Type == MsgProposal && s.rotor != nil && s.rotor.Enabled() {
		eng := s.engine.Engine()
		if eng == nil {
			_ = s.p2p.PublishToTopic(s.ctx, topic, gossipBytes)
			return
		}
		view := eng.CurrentView()
		vs := eng.CurrentValidatorSet()
		leader := LeaderForView(view, vs)

		var ds DirectSender
		if sender, ok := s.p2p.(P2PDirectSender); ok {
			ds = &serviceSender{sender: sender}
		}

		gossipFn := func(d []byte) error {
			return s.p2p.PublishToTopic(s.ctx, topic, d)
		}

		if err := s.rotor.BroadcastViaRelays(s.ctx, view, vs, leader,
			ds, s.rpcTopic, gossipFn, gossipBytes,
		); err != nil {
			log.Warn("hotstuff: rotor broadcast failed, falling back to gossip", "err", err)
			_ = s.p2p.PublishToTopic(s.ctx, topic, gossipBytes)
		}
		return
	}

	publishCtx := s.ctx
	publishOpts := []pubsub.PubOpt(nil)
	var cancel context.CancelFunc
	if output.Message.Type == MsgTimeout {
		// Timeout messages are deterministic for (view, validator). If the first
		// copy is published while only a startup fragment of the mesh is ready,
		// later copies are suppressed by GossipSub's seen cache and the other
		// validators can remain below TC quorum forever. Wait until this node has
		// enough subscribed topic peers to make a quorum with itself. Bound the
		// wait so repeated timeout events cannot leak blocked publishers when the
		// network genuinely lacks quorum.
		if eng := s.engine.Engine(); eng != nil {
			peerTarget := timeoutPublishPeerTarget(eng.CurrentValidatorSet().QuorumSize())
			if peerTarget > 0 {
				publishOpts = append(publishOpts, pubsub.WithReadiness(pubsub.MinTopicSize(peerTarget)))
				publishCtx, cancel = context.WithTimeout(s.ctx, 30*time.Second)
				defer cancel()
			}
		}
	}

	if err := s.p2p.PublishToTopic(publishCtx, topic, gossipBytes, publishOpts...); err != nil {
		log.Warn("hotstuff: broadcast failed", "err", err)
	}
}

// timeoutPublishPeerTarget returns the number of remote topic subscribers a
// validator needs before publishing its deterministic timeout. The sender
// processes its own timeout locally, so quorum-1 remote recipients suffice.
func timeoutPublishPeerTarget(quorumSize int) int {
	if quorumSize <= 1 {
		return 0
	}
	return quorumSize - 1
}

func (s *Service) handleSendToValidator(output EngineOutput) {
	if output.Message == nil || s.p2p == nil {
		return
	}

	// Try direct send if peer registry is available.
	if s.rotor != nil && s.rotor.Enabled() {
		eng := s.engine.Engine()
		if eng != nil {
			vs := eng.CurrentValidatorSet()
			if pid, ok := s.rotor.LookupPeer(vs, output.Target); ok {
				if sender, ok := s.p2p.(P2PDirectSender); ok {
					data, err := EncodeConsensusMsg(output.Message)
					if err == nil {
						// Compress for wire format consistency.
						var buf bytes.Buffer
						enc := s.p2p.Encoding()
						if _, encErr := enc.EncodeGossip(&buf, &rawSSZMarshaler{data: data}); encErr == nil {
							if sendErr := sender.SendRawBytes(s.ctx, buf.Bytes(), s.rpcTopic, pid); sendErr == nil {
								s.rotor.RecordVoteDirect()
								s.logVoteRouting()
								return // direct send succeeded
							}
							// The stream failed — the mapped peer is gone or the
							// connection is dead. Drop the stale mapping now so
							// subsequent sends skip the doomed dial instead of
							// re-timing-out per message; the next gossip message
							// from that validator re-learns the fresh peer.ID
							// (learnValidatorPeer). Safety is unaffected either
							// way — this path already falls back to broadcast.
							if addr, aerr := vs.GetAddress(output.Target); aerr == nil {
								s.rotor.UnregisterValidator(addr)
								log.Debug("hotstuff: dropped stale rotor peer mapping after failed direct send",
									"validator", addr, "peer", pid)
							}
						}
					}
				}
			}
		}
	}

	// Fallback to gossip broadcast.
	if s.rotor != nil {
		s.rotor.RecordVoteFallback()
		s.logVoteRouting()
	}
	s.handleBroadcast(output)
}

// logVoteRouting periodically reports the Rotor direct-send vs fallback ratio
// for targeted vote/timeout sends, so a field deployment can confirm the
// RegisterValidator wiring (see learnValidatorPeer) is actually resolving
// peers — instead of silently falling back to full gossip broadcast for every
// message, as it did before that fix.
func (s *Service) logVoteRouting() {
	direct, fallback := s.rotor.VoteStats()
	total := direct + fallback
	if total <= 5 || total%200 == 0 {
		log.Info("hotstuff: vote routing stats", "direct", direct, "fallback", fallback)
	}
}

// subscribeMessages subscribes to the HotStuff gossip topic and processes incoming messages.
func (s *Service) subscribeMessages() {
	defer s.wg.Done()

	if s.p2p == nil {
		log.Warn("hotstuff: P2P not available, skipping message subscription")
		return
	}

	enc := s.p2p.Encoding()
	topic := s.gossipTopic + enc.ProtocolSuffix()

	sub, err := s.p2p.SubscribeToTopic(topic)
	if err != nil {
		log.Error("hotstuff: failed to subscribe to gossip topic", "err", err)
		return
	}
	defer sub.Cancel()

	log.Info("hotstuff: subscribed to gossip topic", "topic", topic)

	msgCount := 0
	for {
		msg, err := sub.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return // context cancelled
			}
			log.Warn("hotstuff: gossip receive error", "err", err)
			continue
		}

		msgCount++
		if msgCount <= 5 || msgCount%100 == 0 {
			log.Info("hotstuff: received gossip message", "count", msgCount, "bytes", len(msg.Data))
		}
		s.processGossipMessage(msg.Data, enc, msg.ReceivedFrom)
	}
}

func (s *Service) processGossipMessage(data []byte, enc encoder.NetworkEncoding, from peer.ID) {
	// Decompress snappy.
	raw := &rawSSZMarshaler{}
	if err := enc.DecodeGossip(data, raw); err != nil {
		log.Debug("hotstuff: failed to decode gossip message", "err", err)
		return
	}

	consensusMsg, err := DecodeConsensusMsg(raw.data)
	if err != nil {
		log.Debug("hotstuff: failed to decode consensus message", "err", err)
		return
	}

	ce := s.engine.Engine()
	if ce == nil {
		return
	}

	// Learn the (validator address, peer.ID) mapping regardless of whether the
	// engine ends up accepting this specific message. Gating registration on
	// ProcessEvent's success made it depend on view-timing races (a gossiped
	// vote/timeout for a view we've already advanced past returns a non-nil
	// ViewMismatchError even though the message is perfectly legitimate and its
	// sender/peer.ID pairing is still true) — in practice that starved the
	// registry and every send stayed on the fallback broadcast path. The
	// mapping only affects routing, not consensus safety (see learnValidatorPeer),
	// so learning it from any successfully decoded message is sufficient.
	s.learnValidatorPeer(consensusMsg, from)

	if err := ce.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  *consensusMsg,
	}); err != nil {
		log.Debug("hotstuff: message processing error", "type", consensusMsg.Type, "err", err)
	}
}

// messageSenderIndex extracts the originating validator index from a decoded
// consensus message, for the message types that carry one. Used by
// learnValidatorPeer to discover which peer.ID a validator address maps to.
func messageSenderIndex(msg *ConsensusMsg) (ValidatorIndex, bool) {
	if msg == nil {
		return 0, false
	}
	switch p := msg.Payload.(type) {
	case *Proposal:
		return p.Proposer, true
	case *Vote:
		return p.Voter, true
	case *CommitVote:
		return p.Voter, true
	case *TimeoutMessage:
		return p.Sender, true
	case *NewViewMsg:
		return p.Leader, true
	default:
		return 0, false
	}
}

// learnValidatorPeer records the (validator address, peer.ID) mapping Rotor
// needs for direct sends, inferred from a successfully processed consensus
// message: the message names its sender's validator index, and the pubsub/
// relay-stream layer tells us which peer.ID delivered it. This is the only
// call site that ever feeds Rotor's peer registry — without it the registry
// stays empty forever (as it did before this fix), LookupPeer always misses,
// and every OutputSendToValidator falls back to full gossip broadcast instead
// of the intended leader-direct/relay path.
//
// Precision caveat: on the gossip path, `from` is pubsub's ReceivedFrom — the
// LAST-HOP propagator, which on a sparse mesh may not be the message's author
// (on the direct relay-stream path it always is). A mis-mapping is not a
// safety issue: consensus messages are signature-verified regardless of the
// delivering peer, and handleSendToValidator falls back to broadcast when the
// direct send errors — a bad entry costs one wasted send attempt until the
// author's own next direct/adjacent message overwrites it.
func (s *Service) learnValidatorPeer(msg *ConsensusMsg, from peer.ID) {
	if s.rotor == nil || from == "" {
		return
	}
	idx, ok := messageSenderIndex(msg)
	if !ok {
		return
	}
	ce := s.engine.Engine()
	if ce == nil {
		return
	}
	addr, err := ce.CurrentValidatorSet().GetAddress(idx)
	if err != nil {
		return
	}
	s.rotor.RegisterValidator(addr, from)

	events := s.rotor.RegistrationEvents()
	if events <= 10 || events%200 == 0 {
		log.Info("hotstuff: rotor peer registry", "events", events, "knownPeers", s.rotor.RegisteredPeerCount())
	}
}

// pacemakerLoop manages the view timeout timer.
func (s *Service) pacemakerLoop() {
	defer s.wg.Done()

	for {
		ce := s.engine.Engine()
		if ce == nil {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}

		pm := ce.Pacemaker()
		select {
		case <-s.ctx.Done():
			return
		case <-pm.TimeoutChan():
			if err := ce.OnTimeout(); err != nil {
				log.Warn("hotstuff: timeout handling error", "err", err)
			}
			updateMetricsTimeout()
		}
	}
}

// persistState saves the current consensus state to the database.
func (s *Service) persistState() {
	if s.db == nil {
		return
	}

	ce := s.engine.Engine()
	if ce == nil {
		return
	}

	state := &ConsensusState{
		View:                ce.CurrentView(),
		ConsecutiveTimeouts: ce.ConsecutiveTimeouts(),
		LockedQC:            ce.LockedQC(),
		LastCommittedQC:     ce.LastCommittedQC(),
	}

	if err := s.db.Update(s.ctx, func(tx kv.RwTx) error {
		if err := SaveConsensusState(tx, state); err != nil {
			return err
		}
		// Atomically persist staged epoch data in the same transaction.
		if epoch, validators, f, ok := ce.StagedEpochInfoSafe(); ok {
			return SaveStagedEpoch(tx, epoch, validators, f)
		}
		return nil
	}); err != nil {
		log.Warn("hotstuff: failed to persist state", "err", err)
		return
	}

	s.lastPersistedView = state.View
}

// recoverState loads persisted consensus state and reinitializes the engine.
func (s *Service) recoverState() error {
	var state *ConsensusState
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		var err error
		state, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		return err
	}
	if state == nil {
		return nil // no persisted state
	}

	log.Info("hotstuff: recovering persisted state",
		"view", state.View, "timeouts", state.ConsecutiveTimeouts,
		"lockedQCView", state.LockedQC.View, "committedQCView", state.LastCommittedQC.View)

	// The engine was already created by adapter.New(). We need to reinitialize
	// with recovered state if the persisted view is ahead.
	ce := s.engine.Engine()
	if ce == nil {
		return nil
	}
	currentView := ce.CurrentView()
	if state.View > currentView {
		// Sanity check: QC views must not exceed state view.
		if state.LockedQC.View > state.View || state.LastCommittedQC.View > state.View {
			log.Warn("hotstuff: corrupted persisted state — QC view exceeds state view, ignoring")
			return nil
		}
		ce.RestoreState(state.View, state.LockedQC, state.LastCommittedQC, state.ConsecutiveTimeouts)
		log.Info("hotstuff: engine restored to persisted state", "view", state.View)
	}

	return nil
}

// rawSSZMarshaler wraps raw bytes to implement the ssz.Marshaler/Unmarshaler interfaces
// needed by the encoder.NetworkEncoding for gossip compression (snappy).
type rawSSZMarshaler struct {
	data []byte
}

func (r *rawSSZMarshaler) MarshalSSZ() ([]byte, error) {
	return r.data, nil
}

func (r *rawSSZMarshaler) MarshalSSZTo(buf []byte) ([]byte, error) {
	return append(buf, r.data...), nil
}

func (r *rawSSZMarshaler) SizeSSZ() int {
	return len(r.data)
}

func (r *rawSSZMarshaler) UnmarshalSSZ(buf []byte) error {
	r.data = make([]byte, len(buf))
	copy(r.data, buf)
	return nil
}

// serviceSender adapts P2PDirectSender to the Rotor DirectSender interface.
type serviceSender struct {
	sender P2PDirectSender
}

func (ss *serviceSender) SendRawDirect(ctx context.Context, data []byte, topic string, pid peer.ID) error {
	return ss.sender.SendRawBytes(ctx, data, topic, pid)
}

// setupRotorStreamHandler registers the inbound RPC handler for Rotor relay
// messages. When a relay node receives a proposal from the leader, it processes
// it and forwards to its assigned targets.
func (s *Service) setupRotorStreamHandler() {
	sender, ok := s.p2p.(P2PDirectSender)
	if !ok {
		log.Debug("hotstuff: P2P does not support direct send, Rotor relay handler not registered")
		return
	}

	sender.SetStreamHandler(s.rpcTopic, func(data []byte, from peer.ID) {
		// Process the message as if received via gossip.
		enc := s.p2p.Encoding()
		s.processGossipMessage(data, enc, from)

		// If we are a relay for the current view, forward to our assigned targets.
		if s.rotor == nil || !s.rotor.Enabled() {
			return
		}

		ce := s.engine.Engine()
		if ce == nil {
			return
		}

		view := ce.CurrentView()
		vs := ce.CurrentValidatorSet()
		leader := LeaderForView(view, vs)
		myIndex := ce.MyIndex()

		if s.rotor.IsRelay(view, vs, leader, myIndex) {
			ds := &serviceSender{sender: sender}
			s.rotor.ForwardToTargets(s.ctx, view, vs, leader, myIndex,
				ds, s.rpcTopic, data)
		}
	})

	log.Info("hotstuff: Rotor relay stream handler registered", "topic", s.rpcTopic)
}

// Rotor returns the service's Rotor instance for external configuration.
func (s *Service) Rotor() *Rotor {
	return s.rotor
}

// broadcastBlockData is a placeholder for explicit leader block broadcast.
// Currently the miner already broadcasts sealed blocks via the sync layer's
// block gossip topic (SealedBlock → p2p.Broadcast in blockchain.go).
// This method adds a small delay before sending the Proposal to give the
// block gossip time to propagate to followers.
func (s *Service) broadcastBlockData(_ types.Hash) {
	// The miner's SealedBlock already gossips the block on the standard
	// block topic. We just need the Proposal to arrive slightly after,
	// giving followers time to import. A brief yield is sufficient.
	time.Sleep(50 * time.Millisecond)
}

// NotifyBlockImported implements sync.BlockImportNotifier.
// Called by the sync layer after a gossip block is successfully imported.
// Matches against pending execution requests and notifies the engine.
func (s *Service) NotifyBlockImported(hash types.Hash, txHash types.Hash) {
	// Import notifications are execution retry opportunities for a committed
	// block. A successful applied probe clears the production guard; a body-only
	// or future-queued notification counts as another failed observation.
	s.pendingMu.Lock()
	trackExecution := s.unexecutedCommitHash == hash && hash != (types.Hash{})
	s.pendingMu.Unlock()
	if trackExecution {
		s.observeCommittedExecution(hash, false)
	}

	s.pendingMu.Lock()
	delete(s.pendingExecutions, hash) // clear if present; no longer used to gate
	retryCommit := s.pendingCommit == hash && hash != (types.Hash{})
	if retryCommit {
		s.pendingCommit = types.Hash{}
	}
	s.pendingMu.Unlock()

	// A commit that was deferred because this block hadn't arrived: finish it
	// now, so the canonical marker and head advance on every node, not just the
	// ones that held the block at commit time. (Not applied-gated: the block
	// already holds a CommitQC — canonicalizing it needs its body, not its
	// local execution.)
	if retryCommit && s.blockProducer != nil {
		if err := s.blockProducer.CommitToCanonical(hash); err != nil {
			log.Debug("hotstuff: deferred commit-to-canonical retry failed", "hash", hash, "err", err)
		} else {
			log.Info("hotstuff: deferred commit-to-canonical completed", "hash", hash)
		}
	}

	// Best-effort parent lookup for the engine's extends-check (vote must only
	// certify a block that extends its proposal's JustifyQC block). Zero when
	// unavailable — the check fails open.
	var parentHash types.Hash
	var headerExtra []byte
	var blockNum uint64
	haveHeader := false
	if s.db != nil {
		_ = s.db.View(s.ctx, func(tx kv.Tx) error {
			if hdr, herr := rawdb.ReadHeaderByHash(tx, hash); herr == nil && hdr != nil {
				parentHash = hdr.ParentHash
				headerExtra = hdr.Extra
				blockNum = hdr.Number.Uint64()
				haveHeader = true
			}
			return nil
		})
	}
	// Applied-evidence gate: the engine treats EventBlockImported as "this
	// node EXECUTED the block", and the import-gated vote certifies exactly
	// that. Callers historically fired it on body arrival or a nil insert
	// error — a future-queued block returns nil too — which let six
	// validators vote a forked leader's inexecutable block into a CommitQC
	// and wedge the whole network's committed head. No applied state, no
	// engine notification; the dedup set is NOT stamped, so the notify that
	// arrives after the block really executes still passes.
	if !haveHeader {
		return
	}
	if bf, ok := s.blockFetcher.(interface {
		BlockApplied(types.Hash, uint64) bool
	}); ok && !bf.BlockApplied(hash, blockNum) {
		log.Debug("hotstuff: import notification withheld (block not applied)",
			"hash", hash, "number", blockNum)
		return
	}

	// Dedup the engine notification: skip if we already notified this hash.
	// The engine keeps its own imported-block memory across views, so one
	// notify per hash is enough to let the import-gated prepare vote fire;
	// re-notifying on every re-gossip only spins the engine mutex and starves
	// the vote path.
	s.pendingMu.Lock()
	alreadyNotified := false
	if _, ok := s.notifiedImports[hash]; ok {
		alreadyNotified = true
	} else {
		s.notifiedImports[hash] = struct{}{}
		s.notifiedFIFO = append(s.notifiedFIFO, hash)
		if len(s.notifiedFIFO) > maxNotifiedImports {
			oldest := s.notifiedFIFO[0]
			s.notifiedFIFO = s.notifiedFIFO[1:]
			delete(s.notifiedImports, oldest)
		}
	}
	s.pendingMu.Unlock()

	// Notify the engine once per hash (dedup above). Do NOT gate on
	// pendingExecutions, which advanceToView clears every view. With import-gated
	// voting, the engine's onBlockImported decides (via pendingProposals) whether
	// this import unblocks the current view's deferred prepare vote. A re-gossiped
	// already-notified block carries no new information for the engine — the
	// engine retains imported-block memory across views — so re-notifying only
	// spins the engine mutex.
	if alreadyNotified {
		return
	}
	// Catch-up canonicalization: the imported block's extra carries the leader's
	// latest CommitQC — the network's transferable, BLS-verifiable proof that the
	// QC's target block is committed. Live commits flow through
	// OutputBlockCommitted, but a node importing HISTORICAL blocks (range
	// catch-up, future-queue drain) sees views that are long gone, so this is its
	// only legitimate way to advance the canonical head — import-time fork choice
	// is disabled on leader-driven chains (canonical is commit-authority-only).
	s.maybeCommitFromHeaderQC(headerExtra)
	if ce := s.engine.Engine(); ce != nil {
		if err := ce.ProcessEvent(ConsensusEvent{
			Type:       EventBlockImported,
			Hash:       hash,
			TxRootHash: txHash,
			ParentHash: parentHash,
		}); err != nil {
			log.Debug("hotstuff: EventBlockImported processing failed", "hash", hash, "err", err)
		}
	}
}

// maybeCommitFromHeaderQC advances the canonical chain from the CommitQC a
// just-imported block carries in its header extra (buildHeaderExtra embeds the
// leader's LastCommittedQC into every block). Import-time fork choice is
// disabled on leader-driven chains, so a catching-up node's canonical head can
// only follow commit proofs; the embedded QC is exactly that — a 2f+1
// BLS-verifiable statement that its target block is committed — and needs no
// live view context. Cheap height gate first: signature verification runs only
// when the QC's target is locally known AND ahead of the committed head, so the
// synced steady state pays one header read per import. A QC signed by an older
// validator set (cross-epoch catch-up) fails verification and is skipped —
// fail-closed; such a node converges via live commits once its view syncs.
func (s *Service) maybeCommitFromHeaderQC(extra []byte) {
	if s.blockProducer == nil || s.db == nil || len(extra) == 0 {
		return
	}
	qc, err := ExtractHeaderQC(extra)
	if err != nil || qc == nil || qc.View == 0 || qc.BlockHash == (types.Hash{}) {
		return
	}
	var qcNum *uint64
	var committedHead uint64
	_ = s.db.View(s.ctx, func(tx kv.Tx) error {
		qcNum = rawdb.ReadHeaderNumber(tx, qc.BlockHash)
		if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil {
			committedHead = *hn
		}
		return nil
	})
	if qcNum == nil || *qcNum <= committedHead {
		return // target not imported yet, or not ahead of the committed head
	}
	ce := s.engine.Engine()
	if ce == nil {
		return
	}
	if verr := VerifyQCAnyDomain(qc, ce.CurrentValidatorSet()); verr != nil {
		log.Debug("hotstuff: header-QC canonicalize skipped (verify failed)",
			"qcBlock", qc.BlockHash, "qcView", qc.View, "err", verr)
		return
	}
	if cerr := s.blockProducer.CommitToCanonical(qc.BlockHash); cerr != nil {
		log.Debug("hotstuff: header-QC commit-to-canonical deferred", "hash", qc.BlockHash, "err", cerr)
	} else {
		log.Info("hotstuff: canonical advanced from header-embedded CommitQC",
			"number", *qcNum, "hash", qc.BlockHash)
	}
}

// maxNotifiedImports bounds the NotifyBlockImported dedup set (see notifiedImports).
// It MUST stay below the engine's MaxImportedBlocks (64): the sync dedup must
// forget a hash BEFORE the engine forgets it, so that if a long-uncommitted
// block is somehow re-proposed after the engine evicted it, the next re-gossip
// re-notifies and re-populates the engine instead of being deduped into a stall.
const maxNotifiedImports = 32
