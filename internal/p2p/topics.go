package p2p

const (
	// GossipProtocolAndDigest represents the protocol and fork digest prefix in a gossip topic.
	GossipProtocolAndDigest = "/n42/%x/"

	// Message types used as suffixes in gossip topic strings.
	GossipBlockMessage       = "block"
	GossipExitMessage        = "voluntary_exit"
	GossipTransactionMessage = "transaction"

	GossipBlobSidecarMessage        = "blob_sidecar"
	GossipDataColumnMessage         = "data_column_sidecar"
	GossipHotStuffConsensusMessage  = "hotstuff_consensus"
	GossipZKProofMessage            = "zk_proof"
	GossipMobilePacketMessage       = "mobileverify_packet"
	GossipMobileRegistrationMessage = "mobileverify_registration"
	GossipMobileCohortIndexMessage  = "mobileverify_cohort_index"
	GossipMobileCohortCertMessage   = "mobileverify_cohort_cert"

	// Topic format strings combining the protocol prefix with message type.
	BlockTopicFormat              = GossipProtocolAndDigest + GossipBlockMessage
	ExitBlockTopicFormat          = GossipProtocolAndDigest + GossipExitMessage
	TransactionTopicFormat        = GossipProtocolAndDigest + GossipTransactionMessage
	BlobSidecarTopicFormat        = GossipProtocolAndDigest + GossipBlobSidecarMessage
	DataColumnTopicFormat         = GossipProtocolAndDigest + GossipDataColumnMessage
	HotStuffConsensusTopicFormat  = GossipProtocolAndDigest + GossipHotStuffConsensusMessage
	ZKProofTopicFormat            = GossipProtocolAndDigest + GossipZKProofMessage
	MobilePacketTopicFormat       = GossipProtocolAndDigest + GossipMobilePacketMessage
	MobileRegistrationTopicFormat = GossipProtocolAndDigest + GossipMobileRegistrationMessage
	MobileCohortIndexTopicFormat  = GossipProtocolAndDigest + GossipMobileCohortIndexMessage
	MobileCohortCertTopicFormat   = GossipProtocolAndDigest + GossipMobileCohortCertMessage

	// Message relay topics (8 shards, Waku-style)
	GossipMessagePrefix = "message/shard/"
	GossipMessageFormat = "/n42/msg/shard/%d"

	// Store query protocol
	StoreQueryProtocol = "/n42/msg/store_query/1.0.0"
)
