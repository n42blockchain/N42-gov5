// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service pub/sub subscription, gossip dispatch, rotor stream and pacemaker loop.

package hotstuff

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/log"
)

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
		// S17: t_rx, the earliest point these bytes are in this process --
		// right after sub.Next() returns, before processGossipMessage's own
		// decode. No topic validator is registered for this topic anywhere
		// in this package (confirmed by grep, matching 6ch), so sub.Next()
		// returning IS the earliest observable point on the gossip path.
		var tRx time.Time
		if contentionDiagEnabled {
			tRx = time.Now()
		}
		if gossipVoteLane {
			s.dispatchGossipMessage(msg.Data, enc, msg.ReceivedFrom, tRx)
			continue
		}
		s.processGossipMessage(msg.Data, enc, msg.ReceivedFrom, tRx, "gossip")
	}
}

// rotorMapFix keeps a validator's Rotor peer mapping after a direct send
// succeeds (only a failed send drops it), and does not learn a proposer's
// peer from a Proposal that a relay forwarded. Without it, 86% of commit
// votes found no mapping for the leader -- the prepare vote's successful send
// had just dropped it -- and went gossip only (E1N26 run 7). On by default;
// N42_ROTOR_MAP_FIX=0 restores the old behaviour.
var rotorMapFix = envswitch.Not0("N42_ROTOR_MAP_FIX", envswitch.Config, "Default on: keep a validator's Rotor peer mapping after a direct send succeeds.")

// SetRotorMapFixForTest switches rotorMapFix and returns the restore.
func SetRotorMapFixForTest(on bool) func() {
	prev := rotorMapFix
	rotorMapFix = on
	return func() { rotorMapFix = prev }
}

// gossipVoteLane is N42_GOSSIP_VOTE_LANE=1: hand gossiped votes to their own
// goroutines instead of processing them on the serial gossip loop.
//
// The loop calls ProcessEvent inline, so every message waits for the engine
// lock behind the one before it, and the next sub.Next() waits for both.
// On the leader that queue is where its Round 2 went: a follower's commit
// vote took 6 ms from the follower's publish to the leader's sub.Next()
// (every leader/follower pair alike), against 0 ms for the same vote over a
// Rotor stream (one goroutine per stream) and 0.2 ms for the PrepareQC
// gossiped the other way, to followers whose loops were idle (E1N26 run 7).
// Votes need no order among themselves -- the Rotor path already delivers
// them concurrently and out of order with the gossip copies -- so they get
// a goroutine each; every other message stays on the loop, in order.
var gossipVoteLane = envswitch.Is1("N42_GOSSIP_VOTE_LANE", envswitch.Perf, "Hand gossiped votes to their own goroutines.")

// gossipVoteLaneMax bounds the vote goroutines in flight; past it a vote is
// processed on the loop as before, which is the backpressure.
const gossipVoteLaneMax = 64

// dispatchGossipMessage is the gossip loop's entry under gossipVoteLane: it
// decodes the message and runs a Vote or CommitVote on its own goroutine,
// anything else inline.
func (s *Service) dispatchGossipMessage(data []byte, enc encoder.NetworkEncoding, from peer.ID, rxAt time.Time) {
	var tArrive time.Time
	if contentionDiagEnabled {
		tArrive = time.Now()
	}
	msg, ok := decodeConsensusGossip(data, enc)
	if !ok {
		return
	}
	if msg.Type == MsgVote || msg.Type == MsgCommitVote {
		select {
		case s.voteLane <- struct{}{}:
			go func() {
				defer func() { <-s.voteLane }()
				s.handleConsensusMsg(msg, from, tArrive, rxAt, "gossip")
			}()
			return
		default:
		}
	}
	s.handleConsensusMsg(msg, from, tArrive, rxAt, "gossip")
}

// processGossipMessage is the single entry point for every inbound consensus
// message, whichever transport delivered it: the gossip loop (subscribeMessages)
// and the direct Rotor-relay stream handler (setupRotorStreamHandler) both call
// this. S14 (N42_CONTENTION_DIAG=1): tArrive is the first line of this
// handler -- the network "channel receive" the task asked for -- before
// snappy-decompress/RLP-decode, so downstream contention stamps (see
// contentionStamps in view_timing.go) include decode time in their
// lock-wait figure. Zero time.Time when the switch is off, which every
// downstream stamp already treats as "not measured."
//
// S17 adds rxAt/via: rxAt (t_rx) is the caller's OWN, even earlier stamp --
// right after sub.Next() (gossip) or right after the Rotor stream read
// (direct/relay) -- so rxAt precedes tArrive by exactly this function's own
// call overhead (nothing else runs between them: see the "READERS" finding
// in docs/QS_BLOCK_TIME_BUDGET.md 6cl). via names which transport this
// specific copy arrived on.
func (s *Service) processGossipMessage(data []byte, enc encoder.NetworkEncoding, from peer.ID, rxAt time.Time, via string) {
	var tArrive time.Time
	if contentionDiagEnabled {
		tArrive = time.Now()
	}

	consensusMsg, ok := decodeConsensusGossip(data, enc)
	if !ok {
		return
	}
	s.handleConsensusMsg(consensusMsg, from, tArrive, rxAt, via)
}

// decodeConsensusGossip undoes the snappy gossip envelope and decodes the
// consensus message inside.
func decodeConsensusGossip(data []byte, enc encoder.NetworkEncoding) (*ConsensusMsg, bool) {
	raw := &rawSSZMarshaler{}
	if err := enc.DecodeGossip(data, raw); err != nil {
		log.Debug("hotstuff: failed to decode gossip message", "err", err)
		return nil, false
	}
	consensusMsg, err := DecodeConsensusMsg(raw.data)
	if err != nil {
		log.Debug("hotstuff: failed to decode consensus message", "err", err)
		return nil, false
	}
	return consensusMsg, true
}

// handleConsensusMsg learns the sender's peer and hands a decoded message to
// the engine.
func (s *Service) handleConsensusMsg(consensusMsg *ConsensusMsg, from peer.ID, tArrive, rxAt time.Time, via string) {
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
	//
	// A Proposal on a Rotor stream comes from the relay that forwarded it, not
	// from its proposer, so under rotorMapFix it teaches nothing: learning it
	// pointed the leader's mapping at the relay, and the followers' votes went
	// to the relay instead of the leader.
	if !(rotorMapFix && via == "rotor" && consensusMsg.Type == MsgProposal) {
		s.learnValidatorPeer(consensusMsg, from)
	}

	if err := ce.ProcessEvent(ConsensusEvent{
		Type:       EventMessage,
		Msg:        *consensusMsg,
		ReceivedAt: tArrive,
		RxAt:       rxAt,
		Via:        via,
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
		// S17: t_rx, the earliest point these bytes are in this process on
		// the Rotor path. The underlying libp2p stream read completed just
		// before this closure was invoked (internal/node/hotstuff_p2p_adapter.go's
		// SetStreamHandler wrapper reads the full body, then calls this
		// handler with it already in memory) -- one goroutine per incoming
		// stream (libp2p's own SetStreamHandler contract), unlike the
		// single serial reader loop the gossip path uses.
		var tRx time.Time
		if contentionDiagEnabled {
			tRx = time.Now()
		}
		// Process the message as if received via gossip.
		enc := s.p2p.Encoding()
		s.processGossipMessage(data, enc, from, tRx, "rotor")

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
		leader := ce.LeaderForView(view, vs)
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
// block gossip time to propagate to followers (proposalPushGrace).
func (s *Service) broadcastBlockData(_ types.Hash) {
	if d := proposalPushGrace(); d > 0 {
		time.Sleep(d)
	}
}
