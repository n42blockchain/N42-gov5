package p2p

import (
	"fmt"
	"strings"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// pubsubSubscriptionRequestLimit is the max subscriptions per RPC.
// Set to handle double topic subscriptions at fork boundaries:
// 64 Attestation Subnets * 2 + 4 Sync Committee Subnets * 2
// + Block,Aggregate,ProposerSlashing,AttesterSlashing,Exits,SyncContribution * 2.
const pubsubSubscriptionRequestLimit = 200

// CanSubscribe returns true if the topic is of interest and we could subscribe to it.
func (s *Service) CanSubscribe(topic string) bool {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "n42" || parts[4] != encoder.ProtocolSuffixSSZSnappy {
		return false
	}

	topicWithoutEncoding := strings.Join(parts[:4], "/")
	for gt := range gossipTopicMappings {
		if _, err := scanfcheck(topicWithoutEncoding, gt); err == nil {
			return true
		}
	}
	return false
}

// FilterIncomingSubscriptions filters subscription RPCs, rejecting requests
// with too many topics and keeping only topics of interest.
func (s *Service) FilterIncomingSubscriptions(_ peer.ID, subs []*pubsubpb.RPC_SubOpts) ([]*pubsubpb.RPC_SubOpts, error) {
	if len(subs) > pubsubSubscriptionRequestLimit {
		return nil, pubsub.ErrTooManySubscriptions
	}
	return pubsub.FilterSubscriptions(subs, s.CanSubscribe), nil
}

// scanfcheck verifies that input matches the format string via fmt.Sscanf.
func scanfcheck(input, format string) (int, error) {
	var placeholder int
	n := strings.Count(format, "%")
	args := make([]interface{}, n)
	for i := range args {
		args[i] = &placeholder
	}
	return fmt.Sscanf(input, format, args...)
}
