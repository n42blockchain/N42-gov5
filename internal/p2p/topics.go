package p2p

const (
	// GossipProtocolAndDigest represents the protocol and fork digest prefix in a gossip topic.
	GossipProtocolAndDigest = "/n42/%x/"

	// Message types used as suffixes in gossip topic strings.
	GossipBlockMessage       = "block"
	GossipExitMessage        = "voluntary_exit"
	GossipTransactionMessage = "transaction"

	// Topic format strings combining the protocol prefix with message type.
	BlockTopicFormat       = GossipProtocolAndDigest + GossipBlockMessage
	ExitBlockTopicFormat   = GossipProtocolAndDigest + GossipExitMessage
	TransactionTopicFormat = GossipProtocolAndDigest + GossipTransactionMessage
)
