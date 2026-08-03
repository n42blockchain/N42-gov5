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
	"github.com/n42blockchain/N42/internal/tracing"
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

	// Blob sidecar protocol handlers.
	s.registerRPC(p2p.RPCBlobSidecarsByRangeTopicV1, s.blobSidecarsByRangeRPCHandler)
	s.registerRPC(p2p.RPCBlobSidecarsByRootTopicV1, s.blobSidecarsByRootRPCHandler)

	// Snap sync protocol handlers.
	s.registerRPC(p2p.RPCGetAccountRangeTopicV1, s.accountRangeRPCHandler)
	s.registerRPC(p2p.RPCGetStorageRangeTopicV1, s.storageRangeRPCHandler)
	s.registerRPC(p2p.RPCGetCodeTopicV1, s.codeRPCHandler)

	// Witness protocol handler.
	s.registerRPC(p2p.RPCGetBlockWitnessTopicV1, s.witnessRPCHandler)

	// Snapshot protocol handlers.
	s.registerRPC(p2p.RPCGetSnapshotInfoTopicV1, s.snapshotInfoRPCHandler)
	s.registerRPC(p2p.RPCGetSnapshotAccountRangeTopicV1, s.snapshotAccountRangeRPCHandler)
	s.registerRPC(p2p.RPCGetSnapshotStorageRangeTopicV1, s.snapshotStorageRangeRPCHandler)
	s.registerRPC(p2p.RPCGetChangeSetRangeTopicV1, s.changeSetRangeRPCHandler)

	// Block push: reliable leader→peer block delivery. Not via registerRPC (which
	// SSZ-decodes a request message); this is a raw chunked-block stream handled
	// directly by blockPushStreamHandler.
	s.cfg.p2p.SetStreamHandler(
		p2p.RPCBlockPushTopicV1+s.cfg.p2p.Encoding().ProtocolSuffix(),
		s.blockPushStreamHandler,
	)

	// Block-by-hash request: fetch-on-miss server side (raw 32-byte hash in,
	// chunked block out). Handled directly by blockByHashStreamHandler.
	s.cfg.p2p.SetStreamHandler(
		p2p.RPCBlockByHashTopicV1+s.cfg.p2p.Encoding().ProtocolSuffix(),
		s.blockByHashStreamHandler,
	)

	// Pooled-transaction fetch: the body half of hash announcements. Raw
	// request bytes in, one chunked response out, so it does not go through
	// registerRPC either.
	if s.cfg.txPool != nil {
		s.cfg.p2p.SetStreamHandler(
			p2p.RPCPooledTxsByHashTopicV1+s.cfg.p2p.Encoding().ProtocolSuffix(),
			s.pooledTxsByHashStreamHandler,
		)
	}
}

// unregisterHandlers removes all registered RPC stream handlers.
func (s *Service) unregisterHandlers() {
	suffix := s.cfg.p2p.Encoding().ProtocolSuffix()
	topics := []string{
		p2p.RPCBodiesDataTopicV1,
		p2p.RPCBlobSidecarsByRangeTopicV1,
		p2p.RPCBlobSidecarsByRootTopicV1,
		p2p.RPCStatusTopicV1,
		p2p.RPCGoodByeTopicV1,
		p2p.RPCPingTopicV1,
		p2p.RPCGetAccountRangeTopicV1,
		p2p.RPCGetStorageRangeTopicV1,
		p2p.RPCGetCodeTopicV1,
		p2p.RPCGetBlockWitnessTopicV1,
		p2p.RPCGetSnapshotInfoTopicV1,
		p2p.RPCGetSnapshotAccountRangeTopicV1,
		p2p.RPCGetSnapshotStorageRangeTopicV1,
		p2p.RPCGetChangeSetRangeTopicV1,
		p2p.RPCBlockPushTopicV1,
		p2p.RPCBlockByHashTopicV1,
		p2p.RPCPooledTxsByHashTopicV1,
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

		// OpenTelemetry: trace P2P RPC message handling.
		p2pTracer := tracing.Tracer("p2p")
		ctx, otelSpan := tracing.StartSpan(ctx, p2pTracer, "p2p.rpc."+baseTopic)
		otelSpan.SetAttributes(
			tracing.StringAttr("p2p.topic", topic),
			tracing.StringAttr("p2p.peer", stream.Conn().RemotePeer().String()),
		)
		defer otelSpan.End()

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
