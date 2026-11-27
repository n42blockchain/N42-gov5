// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Validate-then-forward for the profile-1 consensus gossip topics (AM36).
//
// go-libp2p-pubsub forwards a message only after every validator registered
// for its topic accepts it. Without a validator a member relays whatever it
// receives before decoding it. The validators here run the decode-level part
// of the handler before the forward:
//
//   - the message decodes as its topic's type with every field in range
//     (AM34 item 3's view bound included); otherwise Reject;
//   - a well-formed message for a view this node has already left is Ignored
//     (not forwarded, no penalty). Ignore also withholds local delivery, so a
//     stale message still inside the late-message window (AM34 item 6) is
//     handed to the engine's ordinary entry point here first, exactly as the
//     handler would have done;
//   - everything else is Accepted and the decoded message rides along in
//     pubsub's ValidatorData, so the handler does not decode it again.
//
// BLS is NOT verified before forwarding (AM36 item 2 makes it optional): the
// handler keeps its verification, batch paths included. The validators take
// no engine lock.

package hotstuff

import (
	"context"
	"errors"
	"fmt"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/log"
)

// pubsubValidatorRegistrar is the optional P2P surface that registers topic
// validators: p2p.Service and the node's hotstuff adapter provide it; test
// doubles usually do not, and then no validator is registered.
type pubsubValidatorRegistrar interface {
	PubSub() *pubsub.PubSub
}

// registerTopicValidator registers val for topic when p exposes the pubsub
// router. It returns the matching unregister function, or nil when nothing
// was registered.
func registerTopicValidator(p any, topic string, val pubsub.ValidatorEx) (func(), error) {
	r, ok := p.(pubsubValidatorRegistrar)
	if !ok {
		return nil, nil
	}
	ps := r.PubSub()
	if ps == nil {
		return nil, nil
	}
	if err := ps.RegisterTopicValidator(topic, val); err != nil {
		return nil, err
	}
	return func() { _ = ps.UnregisterTopicValidator(topic) }, nil
}

// errConsensusField is the cause of every field-range refusal.
var errConsensusField = errors.New("consensus message field out of range")

// checkConsensusMsgFields checks the fields of a decoded consensus message
// that are judged without any consensus state: the AM34 view bound on the
// message and on every certificate it carries, and, on profile 1 (p1 is the
// genesis set; nil elsewhere), the signer index and signature length of an
// individually signed message and the bitmap width of a certificate the
// sender must itself have verified.
//
// A piggybacked SyncInfo TC (HighTC) is only checked for the view bound: an
// honest node records a higher TC it cannot use yet without verifying it
// (processEmbeddedTC) and re-sends it, so its width says nothing about the
// sender.
func checkConsensusMsgFields(msg *ConsensusMsg, p1 *ValidatorSet) error {
	if msg == nil || msg.Payload == nil {
		return fmt.Errorf("%w: empty message", errConsensusField)
	}
	view := messageView(*msg)
	if !certViewValid(view) {
		return fmt.Errorf("%w: view 2^64-1", errConsensusField)
	}
	switch p := msg.Payload.(type) {
	case *Proposal:
		if err := checkSigner(p1, p.Proposer, p.Signature); err != nil {
			return err
		}
		if err := checkCert(p1, p.JustifyQC.View, len(p.JustifyQC.Signers), true); err != nil {
			return err
		}
		if p.PrepareQC != nil {
			return checkCert(p1, p.PrepareQC.View, len(p.PrepareQC.Signers), true)
		}
	case *Vote:
		if err := checkSigner(p1, p.Voter, p.Signature); err != nil {
			return err
		}
		return checkSyncInfoTC(p.HighTC)
	case *CommitVote:
		if err := checkSigner(p1, p.Voter, p.Signature); err != nil {
			return err
		}
		return checkSyncInfoTC(p.HighTC)
	case *PrepareQCMsg:
		return checkCert(p1, p.QC.View, len(p.QC.Signers), true)
	case *TimeoutMessage:
		if err := checkSigner(p1, p.Sender, p.Signature); err != nil {
			return err
		}
		if err := checkCert(p1, p.HighQC.View, len(p.HighQC.Signers), false); err != nil {
			return err
		}
		return checkSyncInfoTC(p.HighTC)
	case *NewViewMsg:
		if err := checkSigner(p1, p.Leader, p.Signature); err != nil {
			return err
		}
		if err := checkCert(p1, p.TimeoutCert.View, len(p.TimeoutCert.Signers), true); err != nil {
			return err
		}
		return checkCert(p1, p.TimeoutCert.HighQC.View, len(p.TimeoutCert.HighQC.Signers), false)
	case *Decide:
		return checkCert(p1, p.CommitQC.View, len(p.CommitQC.Signers), true)
	default:
		return fmt.Errorf("%w: unknown payload %T", errConsensusField, msg.Payload)
	}
	return nil
}

// checkSigner checks, on profile 1, that an individually signed message
// names a member of the genesis set and carries a compressed BLS signature.
func checkSigner(p1 *ValidatorSet, index ValidatorIndex, sig []byte) error {
	if p1 == nil {
		return nil
	}
	if uint64(index) >= uint64(p1.Len()) {
		return fmt.Errorf("%w: signer %d, set %d", errConsensusField, index, p1.Len())
	}
	if len(sig) != blst.BLSSignatureLength {
		return fmt.Errorf("%w: signature of %d bytes", errConsensusField, len(sig))
	}
	return nil
}

// checkCert checks a carried certificate's view bound and, on profile 1 when
// width is set, that a non-genesis certificate's bitmap is the genesis set's
// width (the profile-1 engine refuses any other: profile1CertError).
func checkCert(p1 *ValidatorSet, view ViewNumber, signers int, width bool) error {
	if !certViewValid(view) {
		return fmt.Errorf("%w: certificate for view 2^64-1", errConsensusField)
	}
	if p1 == nil || !width || view == 0 {
		return nil
	}
	if signers != int(p1.Len()) {
		return fmt.Errorf("%w: certificate bitmap %d, set %d", errConsensusField, signers, p1.Len())
	}
	return nil
}

// checkSyncInfoTC checks a piggybacked TC: view bound only (see
// checkConsensusMsgFields). processEmbeddedTC drops a 2^64-1 TC before it
// records anything, so an honest node never re-sends one.
func checkSyncInfoTC(tc *TimeoutCertificate) error {
	if tc == nil {
		return nil
	}
	if !certViewValid(tc.View) {
		return fmt.Errorf("%w: piggybacked TC for view 2^64-1", errConsensusField)
	}
	return nil
}

// gossipAge classifies a well-formed message against this node's view.
type gossipAge uint8

const (
	gossipCurrent gossipAge = iota // current, future, or not judged by view
	gossipLate                     // stale, inside the late-message window
	gossipTooLate                  // stale, beyond the window
)

// classifyGossipAge mirrors processMessage's stale rule: a message for a
// view below the current one is stale, except Decide and NewView, which the
// engine handles at any view. current is the lock-free view hint (zero =
// unknown, judged current). The hint never leads the real view, so a stale
// verdict is final; a message the hint still calls current may turn stale
// before the handler runs, and the engine handles that as before.
func classifyGossipAge(msg *ConsensusMsg, current ViewNumber) gossipAge {
	if msg.Type == MsgDecide || msg.Type == MsgNewView || current == 0 {
		return gossipCurrent
	}
	view := messageView(*msg)
	if view == 0 || view >= current {
		return gossipCurrent
	}
	if current-view <= lateVoteWindow {
		return gossipLate
	}
	return gossipTooLate
}

// decodedGossip is what the consensus topic validator leaves in
// pubsub.Message.ValidatorData for the subscription handler.
type decodedGossip struct {
	msg *ConsensusMsg
}

// lateGossip is a stale consensus message handed from the validator to the
// late-message loop.
type lateGossip struct {
	msg  *ConsensusMsg
	from peer.ID
	rxAt time.Time
}

// lateGossipQueue bounds the validator -> late-loop hand-off. A full queue
// drops the message (it was never going to be forwarded either); the loop
// only ever sees views the node has already left.
const lateGossipQueue = 4096

// decodeConsensusWire decodes one consensus gossip payload: snappy (with the
// profile-1 size cap), then the SSZ envelope and its typed payload.
func (s *Service) decodeConsensusWire(enc encoder.NetworkEncoding, data []byte) (*ConsensusMsg, string, error) {
	raw := &rawSSZMarshaler{}
	if err := s.decodeConsensusGossip(enc, data, raw); err != nil {
		return nil, "gossip", err
	}
	msg, err := DecodeConsensusMsg(raw.data)
	if err != nil {
		return nil, "consensus", err
	}
	return msg, "", nil
}

// consensusTopicValidator returns the consensus topic's pubsub validator.
// late receives the stale messages still inside the late-message window.
//
// Verdicts:
//   - Reject: does not decode (snappy, size cap, SSZ, unknown type), or a
//     field is out of range (checkConsensusMsgFields);
//   - Ignore: no engine yet (nothing to judge with); a stale message
//     (classifyGossipAge), after a late one is queued for the engine;
//   - Accept: everything else, including future views (the engine buffers
//     or view-jumps on them) and every Decide / NewView. A message this node
//     published itself is accepted once it decodes.
func (s *Service) consensusTopicValidator(enc encoder.NetworkEncoding, late chan<- lateGossip) pubsub.ValidatorEx {
	return func(_ context.Context, from peer.ID, m *pubsub.Message) pubsub.ValidationResult {
		var rxAt time.Time
		if contentionDiagEnabled {
			rxAt = time.Now()
		}
		msg, stage, err := s.decodeConsensusWire(enc, m.Data)
		if err != nil {
			s.noteDecodeFailure(stage, "gossip-validate", err)
			return pubsub.ValidationReject
		}
		var ce *ConsensusEngine
		if s.engine != nil {
			ce = s.engine.Engine()
		}
		if ce == nil {
			return pubsub.ValidationIgnore
		}
		if err := checkConsensusMsgFields(msg, ce.p1SetHint.Load()); err != nil {
			s.noteDecodeFailure("fields", "gossip-validate", err)
			return pubsub.ValidationReject
		}
		if !m.Local {
			switch classifyGossipAge(msg, ViewNumber(ce.viewHint.Load())) {
			case gossipLate:
				select {
				case late <- lateGossip{msg: msg, from: from, rxAt: rxAt}:
				default:
				}
				return pubsub.ValidationIgnore
			case gossipTooLate:
				return pubsub.ValidationIgnore
			}
		}
		m.ValidatorData = &decodedGossip{msg: msg}
		return pubsub.ValidationAccept
	}
}

// lateGossipLoop feeds stale messages the validator withheld from the
// subscription into the engine's ordinary entry point, where processMessage
// sends them down its late paths (checkLateVote, checkLateCommitVote,
// checkStaleProposal) and records any piggybacked TC, as it did when the
// handler saw every message.
func (s *Service) lateGossipLoop(late <-chan lateGossip) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case g := <-late:
			s.processConsensusMsg(g.msg, g.from, g.rxAt, time.Time{}, "gossip")
		}
	}
}

// registerConsensusTopicValidator installs the consensus topic validator on
// profile-1 chains. It returns the late-message channel and the unregister
// function, both nil when no validator was registered (other chains, or a
// P2P layer without a pubsub router).
func (s *Service) registerConsensusTopicValidator(topic string, enc encoder.NetworkEncoding) (chan lateGossip, func()) {
	if s.engine == nil || !s.engine.chainConfig.IsProfile1() {
		return nil, nil
	}
	late := make(chan lateGossip, lateGossipQueue)
	unregister, err := registerTopicValidator(s.p2p, topic, s.consensusTopicValidator(enc, late))
	if err != nil {
		log.Error("hotstuff: failed to register consensus topic validator", "topic", topic, "err", err)
		return nil, nil
	}
	if unregister == nil {
		return nil, nil
	}
	return late, unregister
}

// decideTopicValidator returns the H2-v4 Decide topic's pubsub validator.
//
// Verdicts:
//   - Reject: the envelope or its payload does not decode, the payload is not
//     a Decide, the Decide does not match its CommitQC, or a field is out of
//     range;
//   - Ignore: the envelope names another chain (the topic name carries no
//     chain identity, so an honest member of another chain may publish on it);
//   - Accept: everything else. The CommitQC is verified by the handler.
func (f *DecideFollower) decideTopicValidator() pubsub.ValidatorEx {
	return func(_ context.Context, _ peer.ID, m *pubsub.Message) pubsub.ValidationResult {
		env, err := DecodeH2V4Gossip(m.Data, f.identity)
		if err != nil {
			if errors.Is(err, errH2V4IdentityMismatch) {
				return pubsub.ValidationIgnore
			}
			return pubsub.ValidationReject
		}
		d, ok := env.Message.Payload.(*Decide)
		if env.Message.Type != MsgDecide || !ok || d == nil {
			return pubsub.ValidationReject
		}
		if d.View != d.CommitQC.View || d.BlockHash != d.CommitQC.BlockHash {
			return pubsub.ValidationReject
		}
		if err := checkConsensusMsgFields(env.Message, f.set); err != nil {
			return pubsub.ValidationReject
		}
		m.ValidatorData = env
		return pubsub.ValidationAccept
	}
}
