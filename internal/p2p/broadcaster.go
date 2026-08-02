package p2p

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/pkg/errors"
	ssz "github.com/prysmaticlabs/fastssz"
	"go.opencensus.io/trace"
	"google.golang.org/protobuf/proto"
)

// maxBroadcastTime is the maximum time allowed for a broadcast operation.
const maxBroadcastTime = 8 * time.Second

// ErrMessageNotMapped occurs on a Broadcast attempt when a message has not been defined in the
// GossipTypeMapping.
var ErrMessageNotMapped = errors.New("message type is not mapped to a PubSub topic")

// Broadcast a message to the p2p network, the message is assumed to be
// broadcasted to the current fork.
func (s *Service) Broadcast(ctx context.Context, msg proto.Message) error {
	ctx, span := trace.StartSpan(ctx, "p2p.Broadcast")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, maxBroadcastTime)
	defer cancel()

	forkDigest, err := s.currentForkDigest()
	if err != nil {
		return errors.Wrap(err, "could not retrieve fork digest")
	}

	topic, ok := GossipTypeMapping[reflect.TypeOf(msg)]
	if !ok {
		return ErrMessageNotMapped
	}
	castMsg, ok := msg.(ssz.Marshaler)
	if !ok {
		return errors.Errorf("message of %T does not support marshaller interface", msg)
	}
	return s.broadcastObject(ctx, castMsg, fmt.Sprintf(topic, forkDigest))
}

// broadcastObject broadcasts messages to other peers in our gossip mesh.
func (s *Service) broadcastObject(ctx context.Context, obj ssz.Marshaler, topic string) error {
	ctx, span := trace.StartSpan(ctx, "p2p.broadcastObject")
	defer span.End()

	span.AddAttributes(trace.StringAttribute("topic", topic))

	buf := new(bytes.Buffer)
	if _, err := s.Encoding().EncodeGossip(buf, obj); err != nil {
		return errors.Wrap(err, "could not encode message")
	}

	if err := s.PublishToTopic(ctx, topic+s.Encoding().ProtocolSuffix(), buf.Bytes()); err != nil {
		return errors.Wrap(err, "could not publish message")
	}
	return nil
}

// rawGossipBytes carries pre-encoded bytes (an RLP block) through EncodeGossip's
// MarshalSSZ+snappy framing without imposing an SSZ schema.
type rawGossipBytes struct{ data []byte }

func (r *rawGossipBytes) MarshalSSZ() ([]byte, error)             { return r.data, nil }
func (r *rawGossipBytes) MarshalSSZTo(buf []byte) ([]byte, error) { return append(buf, r.data...), nil }
func (r *rawGossipBytes) SizeSSZ() int                            { return len(r.data) }
func (r *rawGossipBytes) UnmarshalSSZ(buf []byte) error {
	r.data = append([]byte(nil), buf...)
	return nil
}

// BroadcastBlock gossips an already-RLP-encoded block to the block topic. Used
// instead of Broadcast(proto.Message) so consensus blocks travel as RLP (ETH
// standard, hash-stable) rather than the schema-limited SSZ of types_pb.Block.
func (s *Service) BroadcastBlock(ctx context.Context, rlpBytes []byte) error {
	ctx, cancel := context.WithTimeout(ctx, maxBroadcastTime)
	defer cancel()
	forkDigest, err := s.currentForkDigest()
	if err != nil {
		return errors.Wrap(err, "could not retrieve fork digest")
	}
	topic := fmt.Sprintf(BlockTopicFormat, forkDigest)
	return s.broadcastObject(ctx, &rawGossipBytes{data: rlpBytes}, topic)
}

// BroadcastTransaction gossips an already-RLP-encoded transaction, the same way
// BroadcastBlock handles blocks. Transactions used to travel as SSZ over the
// generated protobuf type: 55-66% more bytes before compression and 41% more
// on the wire once snappy has run, plus a throwaway proto struct allocated on
// both ends of every hop (33 allocations to encode one transaction, against 2
// for RLP).
func (s *Service) BroadcastTransaction(ctx context.Context, rlpBytes []byte) error {
	ctx, cancel := context.WithTimeout(ctx, maxBroadcastTime)
	defer cancel()
	forkDigest, err := s.currentForkDigest()
	if err != nil {
		return errors.Wrap(err, "could not retrieve fork digest")
	}
	topic := fmt.Sprintf(TransactionTopicFormat, forkDigest)
	return s.broadcastObject(ctx, &rawGossipBytes{data: rlpBytes}, topic)
}
