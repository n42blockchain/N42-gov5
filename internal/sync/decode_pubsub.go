package sync

import (
	"strings"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/pkg/errors"
	ssz "github.com/prysmaticlabs/fastssz"

	"github.com/n42blockchain/N42/internal/p2p"
)

var (
	errNilPubsubMessage = errors.New("nil pubsub message")
	errInvalidTopic     = errors.New("invalid topic format")
	errUnhandledTopic   = errors.New("gossip topic has no decoder")
)

func (s *Service) decodePubsubMessage(msg *pubsub.Message) (ssz.Unmarshaler, error) {
	if msg == nil || msg.Topic == nil || *msg.Topic == "" {
		return nil, errNilPubsubMessage
	}
	topic := *msg.Topic
	if _, err := p2p.ExtractGossipDigest(topic); err != nil {
		return nil, errors.Wrapf(err, "extraction failed for topic: %s", topic)
	}

	topic = strings.TrimSuffix(topic, s.cfg.p2p.Encoding().ProtocolSuffix())
	topic, err := s.replaceForkDigest(topic)
	if err != nil {
		return nil, err
	}

	// Every topic this node subscribes to carries its own encoding, so the
	// message is handed to the subscriber as raw bytes and decoded there.
	//
	// There used to be a fallback that cloned the topic's registered protobuf
	// message and unmarshalled into it. Once each payload had moved to its own
	// encoding, every registration pointed at the same types_pb.H256
	// placeholder, so that fallback would have decoded an unrecognised topic
	// into an H256 and passed it on as if it were valid. Rejecting the topic is
	// the only honest answer.
	switch topic {
	case p2p.BlockTopicFormat, p2p.TransactionTopicFormat, p2p.BlobSidecarTopicFormat:
		raw := &rawSSZBytes{}
		if err := s.cfg.p2p.Encoding().DecodeGossip(msg.Data, raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, errors.Wrapf(errUnhandledTopic, "topic %s", topic)
}

// replaceForkDigest replaces the fork digest in a topic path with a format placeholder.
func (_ *Service) replaceForkDigest(topic string) (string, error) {
	subStrings := strings.Split(topic, "/")
	if len(subStrings) != 4 {
		return "", errInvalidTopic
	}
	subStrings[2] = "%x"
	return strings.Join(subStrings, "/"), nil
}
