package p2p

const (
	// GossipProtocolAndDigest represents the protocol and fork digest prefix in a gossip topic.
	GossipProtocolAndDigest = "/n42/%x/"

	// Message types used as suffixes in gossip topic strings.
	GossipBlockMessage = "block"
	GossipExitMessage  = "voluntary_exit"

	// GossipTransactionMessage carries RLP-encoded transactions. The name is
	// versioned because the payload format changed: it was SSZ over the
	// generated protobuf type, and a node still speaking that would try to
	// UnmarshalSSZ an RLP payload — either an error storm that costs the sender
	// gossip score, or worse a silent mis-parse. A distinct topic makes a
	// version mismatch fail cleanly as "nobody is subscribed" instead, which
	// during a rolling upgrade means transactions simply do not cross between
	// old and new nodes until the roll finishes.
	GossipTransactionMessage = "transaction_v2"

	GossipBlobSidecarMessage        = "blob_sidecar"
	GossipDataColumnMessage         = "data_column_sidecar"
	GossipHotStuffConsensusMessage  = "hotstuff_consensus"
	GossipZKProofMessage            = "zk_proof"
	GossipMobilePacketMessage       = "mobileverify_packet"
	GossipMobileRegistrationMessage = "mobileverify_registration"
	GossipMobileCohortIndexMessage  = "mobileverify_cohort_index"
	GossipMobileCohortRevealMessage = "mobileverify_cohort_reveal"
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
	MobileCohortRevealTopicFormat = GossipProtocolAndDigest + GossipMobileCohortRevealMessage
	MobileCohortCertTopicFormat   = GossipProtocolAndDigest + GossipMobileCohortCertMessage

	// Message relay topics (8 shards, Waku-style). The prefix must be a real
	// substring of GossipMessageFormat, or the scoring switch in
	// gossip_scoring_params.go falls through to the default error and every
	// shard subscribe fails ("msg" != the old "message").
	GossipMessagePrefix = "/n42/msg/shard/"
	GossipMessageFormat = "/n42/msg/shard/%d"

	// Store query protocol
	StoreQueryProtocol = "/n42/msg/store_query/1.0.0"
)
