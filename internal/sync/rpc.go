package sync

import (
	"context"
	"reflect"
	"runtime/debug"
	"time"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	ssz "github.com/prysmaticlabs/fastssz"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/log"
)

// rpcHandler is responsible for handling and responding to any incoming message.
// This method may return an error to internal monitoring, but the error will
// not be relayed to the peer.
type rpcHandler func(context.Context, interface{}, libp2pcore.Stream) error

// registerRPCHandlers registers all p2p RPC stream handlers.
func (s *Service) registerRPCHandlers() {
	s.registerRPC(p2p.RPCStatusTopicV1, s.statusRPCHandler)
	s.registerRPC(p2p.RPCGoodByeTopicV1, s.goodbyeRPCHandler)
	s.registerRPC(p2p.RPCPingTopicV1, s.pingHandler)
	s.registerRPC(p2p.RPCBodiesDataTopicV1, s.bodiesByRangeRPCHandler)

	// Snap sync protocol handlers.
	s.registerRPC(p2p.RPCGetAccountRangeTopicV1, s.accountRangeRPCHandler)
	s.registerRPC(p2p.RPCGetStorageRangeTopicV1, s.storageRangeRPCHandler)
	s.registerRPC(p2p.RPCGetCodeTopicV1, s.codeRPCHandler)
}

// unregisterHandlers removes all registered RPC stream handlers.
func (s *Service) unregisterHandlers() {
	suffix := s.cfg.p2p.Encoding().ProtocolSuffix()
	topics := []string{
		p2p.RPCBodiesDataTopicV1,
		p2p.RPCStatusTopicV1,
		p2p.RPCGoodByeTopicV1,
		p2p.RPCPingTopicV1,
		p2p.RPCGetAccountRangeTopicV1,
		p2p.RPCGetStorageRangeTopicV1,
		p2p.RPCGetCodeTopicV1,
	}
	for _, t := range topics {
		s.cfg.p2p.Host().RemoveStreamHandler(protocol.ID(t + suffix))
	}
}

// registerRPC registers a stream handler for a given topic with an expected protobuf message type.
func (s *Service) registerRPC(baseTopic string, handle rpcHandler) {
	topic := baseTopic + s.cfg.p2p.Encoding().ProtocolSuffix()
	s.cfg.p2p.SetStreamHandler(topic, func(stream network.Stream) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic occurred", "topic", topic, "err", r)
				log.Errorf("%s", debug.Stack())
			}
		}()

		ctx, cancel := context.WithTimeout(s.ctx, ttfbTimeout)
		defer cancel()

		// Resetting after closing is a no-op so defer a reset in case something goes wrong.
		// It's up to the handler to Close the stream (send an EOF) if it successfully writes
		// a response. We don't blindly call Close here because we may have only written a
		// partial response.
		defer func() {
			_ = stream.Reset()
		}()

		ctx, span := trace.StartSpan(ctx, "sync.rpc")
		defer span.End()
		span.AddAttributes(
			trace.StringAttribute("topic", topic),
			trace.StringAttribute("peer", stream.Conn().RemotePeer().String()),
		)

		// Check that the peer is not banned before processing.
		if s.cfg.p2p.Peers().IsBad(stream.Conn().RemotePeer()) {
			if err := s.sendGoodByeAndDisconnect(ctx, p2ptypes.GoodbyeCodeBanned, stream.Conn().RemotePeer()); err != nil {
				log.Debug("Could not disconnect from peer", "peer", stream.Conn().RemotePeer().String(), "topic", stream.Protocol(), "err", err)
			}
			return
		}

		// Validate request according to peer rate limits.
		if err := s.rateLimiter.validateRawRpcRequest(stream); err != nil {
			log.Debug("Could not validate rpc request from peer", "peer", stream.Conn().RemotePeer().String(), "topic", stream.Protocol(), "err", err)
			return
		}
		s.rateLimiter.addRawStream(stream)

		if err := stream.SetReadDeadline(time.Now().Add(ttfbTimeout)); err != nil {
			log.Debug("Could not set stream read deadline", "peer", stream.Conn().RemotePeer().String(), "topic", stream.Protocol(), "err", err)
			return
		}

		base, ok := p2p.RPCTopicMappings[baseTopic]
		if !ok {
			log.Errorf("Could not retrieve base message for topic %s", baseTopic)
			return
		}

		// Decode the incoming message based on whether the registered type is a pointer or value.
		t := reflect.TypeOf(base)
		msg, handlerArg := s.decodeRPCMessage(t, stream, topic)
		if msg == nil {
			return
		}

		messageReceivedCounter.WithLabelValues(topic).Inc()

		if err := handle(ctx, handlerArg, stream); err != nil {
			messageFailedProcessingCounter.WithLabelValues(topic).Inc()
			if err != p2ptypes.ErrWrongForkDigestVersion {
				log.Debug("Could not handle p2p RPC", "topic", topic, "err", err)
			}
		}
	})
}

// decodeRPCMessage decodes the stream into an SSZ message. It returns the decoded message
// (as ssz.Unmarshaler) and the value to pass to the handler. Returns (nil, nil) on failure.
func (s *Service) decodeRPCMessage(t reflect.Type, stream network.Stream, topic string) (ssz.Unmarshaler, interface{}) {
	isPtr := t.Kind() == reflect.Ptr

	var elemType reflect.Type
	if isPtr {
		elemType = t.Elem()
	} else {
		elemType = t
	}

	msg, ok := reflect.New(elemType).Interface().(ssz.Unmarshaler)
	if !ok {
		log.Errorf("message of %T does not support marshaller interface", msg)
		return nil, nil
	}

	if err := s.cfg.p2p.Encoding().DecodeWithMaxLength(stream, msg); err != nil {
		log.Debug("Could not decode stream message", "topic", topic, "err", err)
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		return nil, nil
	}

	// For pointer types, the handler receives the pointer directly.
	// For value types, the handler receives the dereferenced value.
	if isPtr {
		return msg, msg
	}
	return msg, reflect.ValueOf(msg).Elem().Interface()
}
