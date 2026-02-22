package sync

import (
	"context"
	"fmt"
	"time"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ssztype "github.com/n42blockchain/N42/common/types/ssz"
	"github.com/n42blockchain/N42/internal/p2p"
	p2ptypes "github.com/n42blockchain/N42/internal/p2p/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/utils"
)

var backOffTime = map[ssztype.SSZUint64]time.Duration{
	p2ptypes.GoodbyeCodeWrongNetwork:          24 * time.Hour,
	p2ptypes.GoodbyeCodeUnableToVerifyNetwork: 24 * time.Hour,
	p2ptypes.GoodbyeCodeBadScore:              2 * time.Hour,
	p2ptypes.GoodbyeCodeBanned:                1 * time.Hour,
	p2ptypes.GoodbyeCodeClientShutdown:        1 * time.Hour,
	p2ptypes.GoodbyeCodeTooManyPeers:          5 * time.Minute,
	p2ptypes.GoodbyeCodeGenericError:          2 * time.Minute,
}

// goodbyeRPCHandler reads the incoming goodbye rpc message from the peer.
func (s *Service) goodbyeRPCHandler(_ context.Context, msg interface{}, stream libp2pcore.Stream) error {
	SetRPCStreamDeadlines(stream)

	m, ok := msg.(*ssztype.SSZUint64)
	if !ok {
		return fmt.Errorf("wrong message type for goodbye, got %T, wanted *uint64", msg)
	}
	if err := s.rateLimiter.validateRequest(stream, 1); err != nil {
		return err
	}
	s.rateLimiter.add(stream, 1)

	peerID := stream.Conn().RemotePeer().String()
	if len(peerID) > 16 {
		peerID = peerID[:16] + "..."
	}
	log.Debug("Peer disconnecting", "peer", peerID, "reason", goodbyeMessage(*m))
	s.cfg.p2p.Peers().SetNextValidTime(stream.Conn().RemotePeer(), goodByeBackoff(*m))

	return s.cfg.p2p.Disconnect(stream.Conn().RemotePeer())
}

// disconnectBadPeer checks whether a peer is considered bad by any scorer,
// and disconnects the peer with an appropriate goodbye code if so.
func (s *Service) disconnectBadPeer(ctx context.Context, id peer.ID) {
	if !s.cfg.p2p.Peers().IsBad(id) {
		return
	}
	err := s.cfg.p2p.Peers().Scorers().ValidationError(id)
	goodbyeCode := p2ptypes.ErrToGoodbyeCode(err)
	if err == nil {
		goodbyeCode = p2ptypes.GoodbyeCodeBanned
	}
	if err := s.sendGoodByeAndDisconnect(ctx, goodbyeCode, id); err != nil {
		log.Debug("Error when disconnecting with bad peer", "err", err)
	}
}

// sendGoodbye sends a generic goodbye to the peer and disconnects.
func (s *Service) sendGoodbye(ctx context.Context, id peer.ID) error {
	return s.sendGoodByeAndDisconnect(ctx, p2ptypes.GoodbyeCodeGenericError, id)
}

func (s *Service) sendGoodByeAndDisconnect(ctx context.Context, code p2ptypes.RPCGoodbyeCode, id peer.ID) error {
	lock := utils.NewMultilock(id.String())
	lock.Lock()
	defer lock.Unlock()

	// If already disconnected, exit early to prevent redundant streams.
	if s.cfg.p2p.Host().Network().Connectedness(id) == network.NotConnected {
		return nil
	}
	if err := s.sendGoodByeMessage(ctx, code, id); err != nil {
		log.Debug("Could not send goodbye message to peer", "peer", id, "error", err)
	}
	return s.cfg.p2p.Disconnect(id)
}

func (s *Service) sendGoodByeMessage(ctx context.Context, code p2ptypes.RPCGoodbyeCode, id peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()

	topic, err := p2p.TopicFromMessage(p2p.GoodbyeMessageName)
	if err != nil {
		return err
	}
	stream, err := s.cfg.p2p.Send(ctx, &code, topic, id)
	if err != nil {
		return err
	}
	defer closeStream(stream)

	log.Debug("Sending Goodbye message to peer", "Reason", goodbyeMessage(code), "peer", stream.Conn().RemotePeer())

	// Wait for the peer to receive the goodbye and close the stream.
	// We don't check the response -- we just need to wait before disconnecting.
	SetStreamReadDeadline(stream, respTimeout)
	_, _ = stream.Read([]byte{0})

	return nil
}

func goodbyeMessage(num p2ptypes.RPCGoodbyeCode) string {
	if reason, ok := p2ptypes.GoodbyeCodeMessages[num]; ok {
		return reason
	}
	return fmt.Sprintf("unknown goodbye value of %d received", num)
}

// goodByeBackoff determines the backoff duration based on the goodbye code.
func goodByeBackoff(num p2ptypes.RPCGoodbyeCode) time.Time {
	if duration, ok := backOffTime[num]; ok {
		return time.Now().Add(duration)
	}
	return time.Time{}
}
