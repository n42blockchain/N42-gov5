package p2p

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	ssz "github.com/prysmaticlabs/fastssz"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/common/types"
)

// maxBroadcastTime is the maximum time allowed for a broadcast operation.
const maxBroadcastTime = 8 * time.Second

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

// BroadcastTxHashes announces transaction hashes to the mesh. The payload is
// the hashes concatenated, nothing else: every hash is exactly 32 bytes, so
// the count is the length divided by 32 and no framing is needed.
//
// This replaces broadcasting bodies. A body used to be published once per
// transaction and then forwarded along every mesh edge, so a 7-node mesh moved
// each transaction's full bytes about six times more than necessary; here the
// mesh carries 32 bytes and a peer that lacks the transaction fetches the body
// once, from one peer, over RPCPooledTxsByHashTopicV1.
func (s *Service) BroadcastTxHashes(ctx context.Context, hashes []types.Hash) error {
	if len(hashes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, maxBroadcastTime)
	defer cancel()
	forkDigest, err := s.currentForkDigest()
	if err != nil {
		return errors.Wrap(err, "could not retrieve fork digest")
	}
	buf := make([]byte, 0, len(hashes)*types.HashLength)
	for i := range hashes {
		buf = append(buf, hashes[i][:]...)
	}
	topic := fmt.Sprintf(TxHashesTopicFormat, forkDigest)
	return s.broadcastObject(ctx, &rawGossipBytes{data: buf}, topic)
}
