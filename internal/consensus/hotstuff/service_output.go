// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service output handling: engine outputs, broadcasts and validator sends.

package hotstuff

import (
	"bytes"
	"context"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// processOutputs reads EngineOutput actions and dispatches them.
func (s *Service) processOutputs() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case output := <-s.engine.OutputCh():
			// S17 (docs/QS_BLOCK_TIME_BUDGET.md 6ck): t_deq, the moment this
			// serial loop dequeues the output -- emit2Deq = t_deq - t_emit.
			// Nil unless the message actually carries a t_emit (i.e. the
			// diagnostic is on), so the zero-value case costs one time.Now().
			var tDeq time.Time
			if contentionDiagEnabled && !output.EmittedAt.IsZero() {
				tDeq = time.Now()
			}
			s.handleOutput(output, tDeq)
		case <-s.productionRetry:
			s.retryBlockProduction()
		}
	}
}

func (s *Service) handleOutput(output EngineOutput, tDeq time.Time) {
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
		go func(out EngineOutput, deq time.Time) {
			// Leader: broadcast block data via gossip BEFORE sending Proposal,
			// so followers can import the block and vote on it.
			if out.Message != nil && out.Message.Type == MsgProposal {
				s.broadcastBlockData(out.Hash)
			}
			s.handleBroadcast(out, deq)
		}(output, tDeq)
	case OutputSendToValidator:
		go s.handleSendToValidator(output, tDeq)
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
		if s.blockFetcher != nil && !s.availableHere(output.Hash) {
			if d := fetchOnMissGrace(); d > 0 {
				// The leader dispatches the push and the Proposal together
				// (proposalPushGrace is zero), so a Proposal that lands first
				// usually has its block a few ms behind: fetch only if it is
				// still missing after the grace.
				s.scheduleFetchOnMiss(output.Hash, d)
			} else {
				s.blockFetcher.FetchBlockByHash(output.Hash)
			}
		}
	case OutputSpeculativeBuild:
		// Advisory: this node leads the next view; start building on the block
		// it just voted for so the proposal is ready at the view change. The
		// producer may drop or abort the guess at any time.
		if sp, ok := s.blockProducer.(SpeculativeBlockProducer); ok && sp != nil {
			sp.PrepareSpeculativeBlock(output.Hash)
		}
	case OutputBlockCommitted:
		if s.commitAsync() {
			s.enqueueCommit(output)
			return
		}
		s.handleBlockCommitted(output)
	case OutputViewChanged:
		s.pendingProduction = nil
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
		log.Info("hotstuff: view changed", "view", output.View, "isLeader", isLeader, "hasProducer", s.blockProducer != nil, "tMs", time.Now().UnixMilli())
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
		if output.View-s.getLastPersistedView() >= s.persistInterval {
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

		// Persist the now-ACTIVE validator set so a node that restarts after this
		// reconfiguration restores the reconfigured set instead of reverting to the
		// genesis set (in-memory-only reconfig would otherwise leave a restarted node
		// unable to verify QCs from the new set — a chain-stalling bitmap mismatch).
		// Atomically clear the consumed staged record in the same transaction.
		if s.db != nil {
			if ce := s.engine.Engine(); ce != nil {
				if epoch, validators, f, ok := ce.CurrentEpochInfoSafe(); ok {
					if err := s.db.Update(s.ctx, func(tx kv.RwTx) error {
						return ActivatePersistedEpoch(tx, epoch, validators, f)
					}); err != nil {
						log.Error("failed to atomically persist active epoch transition", "err", err)
					}
				} else {
					log.Error("hotstuff: epoch transition has no active validator set; staged epoch retained",
						"epoch", output.NewEpoch)
				}
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

// handleBroadcast publishes a broadcast output (Proposal/PrepareQC/Decide/
// Timeout/NewView) to the gossip topic, using Rotor's single-hop relay for
// Proposals when available. tDeq is S17's dequeue stamp from processOutputs;
// zero when called from handleSendToValidator's own "gossip is always also
// sent" fallback (that call site records its OWN stamp for MsgVote/
// MsgCommitVote, so this function's tracking -- gated to Proposal/PrepareQC,
// which never arrive via that path -- never double-counts).
func (s *Service) handleBroadcast(output EngineOutput, tDeq time.Time) {
	if output.Message == nil || s.p2p == nil {
		return
	}
	msgType := output.Message.Type
	trackSend := contentionDiagEnabled && !output.EmittedAt.IsZero() &&
		(msgType == MsgProposal || msgType == MsgPrepareQC)
	var tPub0 time.Time
	path := "gossip only"
	if trackSend {
		defer func() {
			tPub1 := time.Now()
			if eng := s.engine.Engine(); eng != nil {
				eng.recordSendStamp(messageView(*output.Message), msgType, output.EmittedAt, tDeq, tPub0, tPub1, path)
			}
		}()
	}

	if output.Message.Type == MsgDecide && s.h2V4Identity != nil {
		go s.publishH2V4Decide(output.Message)
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

	// Every consensus message emits one of these, several per view, which made
	// it 15% of all log bytes on this fleet. Broadcasting is the normal case and
	// says nothing on its own; the interesting events (a view that stalls, a
	// message that fails to encode or publish) are logged separately.
	log.Debug("hotstuff: broadcasting consensus message", "type", output.Message.Type, "topic", topic, "bytes", len(gossipBytes))

	if trackSend {
		tPub0 = time.Now()
	}

	// Use Rotor single-hop relay for proposal broadcasts.
	if output.Message.Type == MsgProposal && s.rotor != nil && s.rotor.Enabled() {
		eng := s.engine.Engine()
		if eng == nil {
			_ = s.p2p.PublishToTopic(s.ctx, topic, gossipBytes)
			return
		}
		view := eng.CurrentView()
		vs := eng.CurrentValidatorSet()
		leader := eng.LeaderForView(view, vs)

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
			path = "rotor failed -> gossip"
			_ = s.p2p.PublishToTopic(s.ctx, topic, gossipBytes)
		} else {
			path = "rotor ok"
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

const h2V4GossipTopic = "/n42/h2/4/ssz_snappy"

func (s *Service) publishH2V4Decide(message *ConsensusMsg) {
	data, err := EncodeH2V4Gossip(H2V4Envelope{
		Identity: *s.h2V4Identity,
		// The first interoperable profile is intentionally static-validator.
		// Dynamic validator-change encoding is a later protocol revision.
		ChangesHash: types.Hash{},
		Message:     message,
	})
	if err != nil {
		log.Error("hotstuff: failed to encode H2-v4 Decide", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	if err := s.p2p.PublishToTopic(ctx, h2V4GossipTopic, data); err != nil {
		log.Debug("hotstuff: H2-v4 Decide publish skipped", "err", err)
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

// handleSendToValidator sends a targeted output (a prepare/commit vote or a
// timeout), trying Rotor's direct stream first and always ALSO gossiping
// (see the comment above the fallback below). tDeq is S17's dequeue stamp
// from processOutputs; diagnostic and message-type-gated (MsgVote/
// MsgCommitVote only -- Proposal/PrepareQC are tracked in handleBroadcast
// instead, since they arrive here only via OutputBroadcast, not this path).
func (s *Service) handleSendToValidator(output EngineOutput, tDeq time.Time) {
	if output.Message == nil || s.p2p == nil {
		return
	}

	msgType := output.Message.Type
	trackSend := contentionDiagEnabled && !output.EmittedAt.IsZero() &&
		(msgType == MsgVote || msgType == MsgCommitVote)
	rotorAttempted := false
	var tPub0 time.Time
	if trackSend {
		tPub0 = time.Now()
	}

	directDelivered := false
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
							rotorAttempted = true
							sendErr := sender.SendRawBytes(s.ctx, buf.Bytes(), s.rpcTopic, pid)
							if sendErr == nil {
								s.rotor.RecordVoteDirect()
								s.logVoteRouting()
								directDelivered = true
								// Do not return here. GossipSub is configured StrictNoSign/
								// WithNoAuthor, so learnValidatorPeer can only associate a
								// validator with the last-hop relayer, not authenticate that
								// the mapped peer owns the validator key. A wrong peer can
								// accept the stream successfully and black-hole the vote.
								// Keep direct delivery as a latency optimization, with gossip
								// as the mandatory liveness path until peer bindings are signed.
							}
							// The stream failed — the mapped peer is gone or the
							// connection is dead. Drop the stale mapping now so
							// subsequent sends skip the doomed dial instead of
							// re-timing-out per message; the next gossip message
							// from that validator re-learns the fresh peer.ID
							// (learnValidatorPeer). Safety is unaffected either
							// way — this path already falls back to broadcast.
							//
							// Without rotorMapFix this also runs after a send that
							// succeeded, so every direct vote dropped its target's
							// mapping and the next vote to the same leader went
							// gossip only.
							if sendErr != nil || !rotorMapFix {
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
	}

	// Gossip is always sent. It is a fallback when direct delivery failed or no
	// mapping existed, and a safety net when an unauthenticated mapping accepted
	// the direct stream but does not belong to the target validator.
	if s.rotor != nil && !directDelivered {
		s.rotor.RecordVoteFallback()
		s.logVoteRouting()
	}
	// tDeq2 is handleBroadcast's own dequeue-equivalent stamp for this
	// nested gossip send: from this function's perspective the message was
	// already "dequeued" at entry (tDeq), so pass a zero time here to keep
	// handleBroadcast's OWN recordSendStamp (gated to Proposal/PrepareQC)
	// from double-counting a vote passing through it.
	s.handleBroadcast(output, time.Time{})

	if trackSend {
		tPub1 := time.Now()
		path := "gossip only"
		switch {
		case directDelivered:
			path = "rotor ok"
		case rotorAttempted:
			path = "rotor failed -> gossip"
		}
		if eng := s.engine.Engine(); eng != nil {
			eng.recordSendStamp(messageView(*output.Message), msgType, output.EmittedAt, tDeq, tPub0, tPub1, path)
		}
	}
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
