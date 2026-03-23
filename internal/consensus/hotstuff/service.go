// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	vm "github.com/n42blockchain/N42/internal/vm"
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
	TriggerBlockProduction()
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

	blockProducer BlockProducer

	// Rotor single-hop relay for proposal broadcast.
	rotor *Rotor

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Rate-limit state persistence (persist every N views or on commit).
	lastPersistedView ViewNumber
	persistInterval   uint64
}

// NewService creates a new HotStuff service.
func NewService(engine *HotStuff, p2p P2PPublisher, db kv.RwDB, gossipTopic, rpcTopic string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		engine:          engine,
		p2p:             p2p,
		db:              db,
		gossipTopic:     gossipTopic,
		rpcTopic:        rpcTopic,
		rotor:           NewRotor(3),
		ctx:             ctx,
		cancel:          cancel,
		persistInterval: 10,
	}
}

// SetBlockProducer sets the block producer for leader-driven block production.
func (s *Service) SetBlockProducer(bp BlockProducer) {
	s.blockProducer = bp
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
		s.handleBroadcast(output)
	case OutputSendToValidator:
		s.handleSendToValidator(output)
	case OutputExecuteBlock:
		log.Debug("hotstuff: execute block requested", "hash", output.Hash)
		// Block execution is handled by the miner/block processor.
	case OutputBlockCommitted:
		log.Info("hotstuff: block committed", "view", output.View, "hash", output.Hash)
		updateMetricsBlockCommitted(output.View)
		// Mark pending reconfigurations as committed now that the block has a CommitQC.
		if rm := s.engine.Engine().ReconfigManager(); rm != nil && rm.HasPendingChanges() {
			rm.MarkCommitted()
		}
		s.persistState()

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
			vm.SetBlockRandomness(randomness)
		}
	case OutputViewChanged:
		log.Debug("hotstuff: view changed", "view", output.View)
		updateMetricsViewChanged(output.View)
		// Trigger block production if we are the new leader.
		if s.engine.Engine().IsCurrentLeader() && s.blockProducer != nil {
			cfg := s.engine.Config()
			if cfg != nil && cfg.FastPropose {
				// Fast Propose: skip slot boundary wait, propose after minimum delay.
				// Reduces consensus latency by ~72% (1950ms → 551ms typical).
				delay := time.Duration(cfg.MinProposeDelayMs) * time.Millisecond
				if delay == 0 {
					delay = 200 * time.Millisecond
				}
				capturedView := output.View
				go func() {
					select {
					case <-time.After(delay):
						if s.engine.Engine().CurrentView() == capturedView {
							s.blockProducer.TriggerBlockProduction()
						}
					case <-s.ctx.Done():
						return
					}
				}()
			} else {
				s.blockProducer.TriggerBlockProduction()
			}
		}
		// Rate-limited persistence.
		if output.View-s.lastPersistedView >= s.persistInterval {
			s.persistState()
		}
	case OutputSyncRequired:
		log.Warn("hotstuff: sync required", "localView", output.LocalView, "targetView", output.TargetView)
	case OutputEquivocationDetected:
		log.Warn("hotstuff: EQUIVOCATION detected",
			"view", output.View, "validator", output.Validator,
			"hash1", output.Hash1, "hash2", output.Hash2)
		updateMetricsEquivocation()
	case OutputEpochTransition:
		log.Info("hotstuff: epoch transition", "epoch", output.NewEpoch, "validators", output.ValidatorCount)
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

	if err := s.p2p.PublishToTopic(s.ctx, topic, gossipBytes); err != nil {
		log.Warn("hotstuff: broadcast failed", "err", err)
	}
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
								return // direct send succeeded
							}
						}
					}
				}
			}
		}
	}

	// Fallback to gossip broadcast.
	s.handleBroadcast(output)
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

	for {
		msg, err := sub.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return // context cancelled
			}
			log.Warn("hotstuff: gossip receive error", "err", err)
			continue
		}

		s.processGossipMessage(msg.Data, enc)
	}
}

func (s *Service) processGossipMessage(data []byte, enc encoder.NetworkEncoding) {
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

	if err := ce.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  *consensusMsg,
	}); err != nil {
		log.Debug("hotstuff: message processing error", "type", consensusMsg.Type, "err", err)
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
		return SaveConsensusState(tx, state)
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
		s.processGossipMessage(data, enc)

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
