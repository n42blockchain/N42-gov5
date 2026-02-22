package p2p

import (
	"context"
	"fmt"

	"github.com/kr/pretty"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ssz "github.com/prysmaticlabs/fastssz"
	"go.opencensus.io/trace"
)

// Send a message to a specific peer. The returned stream may be used for reading,
// but has been closed for writing. The caller must Close or Reset the stream when done.
func (s *Service) Send(ctx context.Context, message any, baseTopic string, pid peer.ID) (network.Stream, error) {
	ctx, span := trace.StartSpan(ctx, "p2p.Send")
	defer span.End()

	if err := VerifyTopicMapping(baseTopic, message); err != nil {
		return nil, err
	}
	topic := baseTopic + s.Encoding().ProtocolSuffix()
	span.AddAttributes(trace.StringAttribute("topic", topic))

	log.Trace(fmt.Sprintf("Sending RPC request to peer %s", pid.String()), "topic", topic, "request", pretty.Sprint(message))

	ctx, cancel := context.WithTimeout(ctx, maxDialTimeout)
	defer cancel()

	stream, err := s.host.NewStream(ctx, pid, protocol.ID(topic))
	if err != nil {
		return nil, err
	}

	castedMsg, ok := message.(ssz.Marshaler)
	if !ok {
		return nil, fmt.Errorf("%T does not support the ssz marshaller interface", message)
	}
	if _, err := s.Encoding().EncodeWithMaxLength(stream, castedMsg); err != nil {
		if resetErr := stream.Reset(); resetErr != nil {
			log.Debug("Failed to reset stream after encode error", "err", resetErr)
		}
		return nil, err
	}
	if err := stream.CloseWrite(); err != nil {
		if resetErr := stream.Reset(); resetErr != nil {
			log.Debug("Failed to reset stream after close write error", "err", resetErr)
		}
		return nil, err
	}

	return stream, nil
}
