package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/proto/sync_pb"
	ssztype "github.com/n42blockchain/N42/common/types/ssz"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
)

// pingHandler reads the incoming ping rpc message from the peer.
func (s *Service) pingHandler(_ context.Context, msg interface{}, stream libp2pcore.Stream) error {
	SetRPCStreamDeadlines(stream)

	m, ok := msg.(*ssztype.SSZUint64)
	if !ok {
		return fmt.Errorf("wrong message type for ping, got %T, wanted *uint64", msg)
	}
	if err := s.rateLimiter.validateRequest(stream, 1); err != nil {
		return err
	}
	s.rateLimiter.add(stream, 1)

	valid, err := s.validateSequenceNum(*m, stream.Conn().RemotePeer())
	if err != nil {
		if errors.Is(err, p2ptypes.ErrInvalidSequenceNum) {
			s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
			s.writeErrorResponseToStream(responseCodeInvalidRequest, p2ptypes.ErrInvalidSequenceNum.Error(), stream)
		}
		return err
	}

	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		return err
	}
	sq := s.cfg.p2p.GetPing()
	if _, err := s.cfg.p2p.Encoding().EncodeWithMaxLength(stream, sq); err != nil {
		return err
	}
	closeStream(stream)

	if valid {
		s.cfg.p2p.Peers().SetPing(stream.Conn().RemotePeer(), &sync_pb.Ping{SeqNumber: uint64(*m)})
	}
	return nil
}

func (s *Service) sendPingRequest(ctx context.Context, id peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()

	pingReq := ssztype.SSZUint64(s.cfg.p2p.GetPing().SeqNumber)
	topic, err := p2p.TopicFromMessage(p2p.PingMessageName)
	if err != nil {
		return err
	}
	stream, err := s.cfg.p2p.Send(ctx, &pingReq, topic, id)
	if err != nil {
		return err
	}
	sendTime := time.Now()
	defer closeStream(stream)

	code, errMsg, err := ReadStatusCode(stream, s.cfg.p2p.Encoding())
	if err != nil {
		return err
	}

	// Record the round-trip latency for this peer.
	s.cfg.p2p.Host().Peerstore().RecordLatency(id, time.Since(sendTime))

	if code != 0 {
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		return errors.New(errMsg)
	}

	pingResponse := new(ssztype.SSZUint64)
	if err := s.cfg.p2p.Encoding().DecodeWithMaxLength(stream, pingResponse); err != nil {
		return err
	}
	valid, err := s.validateSequenceNum(*pingResponse, stream.Conn().RemotePeer())
	if err != nil {
		if errors.Is(err, p2ptypes.ErrInvalidSequenceNum) {
			s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		}
		return err
	}
	if !valid {
		s.cfg.p2p.Peers().SetPing(stream.Conn().RemotePeer(), &sync_pb.Ping{SeqNumber: uint64(*pingResponse)})
	}
	return nil
}

// validateSequenceNum validates the peer's sequence number against the stored value.
// Returns true if the sequence number matches or advances the known value, false if it is new.
func (s *Service) validateSequenceNum(seq ssztype.SSZUint64, id peer.ID) (bool, error) {
	md, err := s.cfg.p2p.Peers().GetPing(id)
	if err != nil {
		return false, err
	}
	if md == nil {
		return true, nil
	}
	if md.GetSeqNumber() > uint64(seq) {
		return false, p2ptypes.ErrInvalidSequenceNum
	}
	return true, nil
}
