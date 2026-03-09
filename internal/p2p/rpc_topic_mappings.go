package p2p

import (
	"reflect"

	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	ssztype "github.com/n42blockchain/N42/common/types/ssz"
)

// SchemaVersionV1 is the schema version for the RPC protocol ID.
const SchemaVersionV1 = "/1"

// protocolPrefix is the path prefix for all Req/Resp RPC topics.
const protocolPrefix = "/rpc"

// RPC message names used in topic construction.
const (
	StatusMessageName         = "/status"
	GoodbyeMessageName        = "/goodbye"
	PingMessageName           = "/ping"
	BodiesByRangeMessageName  = "/bodies_by_range"
	HeadersByRangeMessageName = "/headers_by_range"

	// Snap sync protocol message names.
	GetAccountRangeMessageName = "/get_account_range"
	GetStorageRangeMessageName = "/get_storage_range"
	GetCodeMessageName         = "/get_code"

	// Blob sidecar protocol message names.
	BlobSidecarsByRangeMessageName = "/blob_sidecars_by_range"
	BlobSidecarsByRootMessageName  = "/blob_sidecars_by_root"

	// Snapshot protocol message names.
	GetSnapshotInfoMessageName         = "/get_snapshot_info"
	GetSnapshotAccountRangeMessageName = "/get_snapshot_account_range"
	GetSnapshotStorageRangeMessageName = "/get_snapshot_storage_range"
	GetChangeSetRangeMessageName       = "/get_changeset_range"
)

// V1 RPC topic constants.
const (
	RPCStatusTopicV1      = protocolPrefix + StatusMessageName + SchemaVersionV1
	RPCGoodByeTopicV1     = protocolPrefix + GoodbyeMessageName + SchemaVersionV1
	RPCPingTopicV1        = protocolPrefix + PingMessageName + SchemaVersionV1
	RPCBodiesDataTopicV1  = protocolPrefix + BodiesByRangeMessageName + SchemaVersionV1
	RPCHeadersDataTopicV1 = protocolPrefix + HeadersByRangeMessageName + SchemaVersionV1

	// Blob sidecar protocol topics.
	RPCBlobSidecarsByRangeTopicV1 = protocolPrefix + BlobSidecarsByRangeMessageName + SchemaVersionV1
	RPCBlobSidecarsByRootTopicV1  = protocolPrefix + BlobSidecarsByRootMessageName + SchemaVersionV1

	// Snap sync protocol topics.
	RPCGetAccountRangeTopicV1 = protocolPrefix + GetAccountRangeMessageName + SchemaVersionV1
	RPCGetStorageRangeTopicV1 = protocolPrefix + GetStorageRangeMessageName + SchemaVersionV1
	RPCGetCodeTopicV1         = protocolPrefix + GetCodeMessageName + SchemaVersionV1

	// Snapshot protocol topics.
	RPCGetSnapshotInfoTopicV1         = protocolPrefix + GetSnapshotInfoMessageName + SchemaVersionV1
	RPCGetSnapshotAccountRangeTopicV1 = protocolPrefix + GetSnapshotAccountRangeMessageName + SchemaVersionV1
	RPCGetSnapshotStorageRangeTopicV1 = protocolPrefix + GetSnapshotStorageRangeMessageName + SchemaVersionV1
	RPCGetChangeSetRangeTopicV1       = protocolPrefix + GetChangeSetRangeMessageName + SchemaVersionV1
)

// RPCTopicMappings maps each RPC topic to its expected request message type.
var RPCTopicMappings = map[string]interface{}{
	RPCStatusTopicV1:     new(sync_pb.Status),
	RPCBodiesDataTopicV1: new(sync_pb.BodiesByRangeRequest),
	RPCPingTopicV1:       new(ssztype.SSZUint64),
	RPCGoodByeTopicV1:    new(ssztype.SSZUint64),

	// Blob sidecar protocol mappings.
	RPCBlobSidecarsByRangeTopicV1: new(sync_pb.BlobSidecarsByRangeRequest),
	RPCBlobSidecarsByRootTopicV1:  new(sync_pb.BlobSidecarsByRootRequest),

	// Snap sync protocol mappings.
	RPCGetAccountRangeTopicV1: new(sync_pb.GetAccountRangeRequest),
	RPCGetStorageRangeTopicV1: new(sync_pb.GetStorageRangeRequest),
	RPCGetCodeTopicV1:         new(sync_pb.GetCodeRequest),

	// Snapshot protocol mappings.
	RPCGetSnapshotInfoTopicV1:         new(sync_pb.GetSnapshotInfoRequest),
	RPCGetSnapshotAccountRangeTopicV1: new(sync_pb.GetSnapshotAccountRangeRequest),
	RPCGetSnapshotStorageRangeTopicV1: new(sync_pb.GetSnapshotStorageRangeRequest),
	RPCGetChangeSetRangeTopicV1:       new(sync_pb.GetChangeSetRangeRequest),
}

// Lookup tables for topic deconstruction.
var (
	protocolMapping = map[string]bool{
		protocolPrefix: true,
	}
	messageMapping = map[string]bool{
		StatusMessageName:          true,
		GoodbyeMessageName:         true,
		PingMessageName:            true,
		BodiesByRangeMessageName:   true,
		HeadersByRangeMessageName:  true,
		BlobSidecarsByRangeMessageName: true,
		BlobSidecarsByRootMessageName:  true,
		GetAccountRangeMessageName: true,
		GetStorageRangeMessageName: true,
		GetCodeMessageName:         true,

		GetSnapshotInfoMessageName:         true,
		GetSnapshotAccountRangeMessageName: true,
		GetSnapshotStorageRangeMessageName: true,
		GetChangeSetRangeMessageName:       true,
	}
	versionMapping = map[string]bool{
		SchemaVersionV1: true,
	}
)

// VerifyTopicMapping verifies that a topic's accompanying message type
// matches its registered mapping.
func VerifyTopicMapping(topic string, msg interface{}) error {
	msgType, ok := RPCTopicMappings[topic]
	if !ok {
		return errors.New("rpc topic is not registered currently")
	}
	receivedType := reflect.TypeOf(msg)
	registeredType := reflect.TypeOf(msgType)
	if !registeredType.AssignableTo(receivedType) {
		return errors.Errorf("accompanying message type is incorrect for topic: wanted %v but got %v",
			registeredType, receivedType)
	}
	return nil
}

// TopicDeconstructor splits an RPC topic into its three logical components:
// protocol prefix, message name, and schema version.
// Input format: /protocol-prefix/message-name/schema-version/...
func TopicDeconstructor(topic string) (string, string, string, error) {
	origTopic := topic

	protPrefix := matchAndConsume(&topic, protocolMapping)
	if protPrefix == "" {
		return "", "", "", errors.Errorf("unable to find a valid protocol prefix for %s", origTopic)
	}

	message := matchAndConsume(&topic, messageMapping)
	if message == "" {
		return "", "", "", errors.Errorf("unable to find a valid message for %s", origTopic)
	}

	version := matchAndConsume(&topic, versionMapping)
	if version == "" {
		return "", "", "", errors.Errorf("unable to find a valid schema version for %s", origTopic)
	}

	return protPrefix, message, version, nil
}

// matchAndConsume finds the first key from mapping that is a prefix of *s,
// consumes it from *s, and returns the matched key. Returns "" if no match.
func matchAndConsume(s *string, mapping map[string]bool) string {
	for k := range mapping {
		if len(k) <= len(*s) && (*s)[:len(k)] == k {
			*s = (*s)[len(k):]
			return k
		}
	}
	return ""
}

// RPCTopic represents a req/resp topic string with convenience methods.
type RPCTopic string

// ProtocolPrefix returns the protocol prefix component of the RPC topic.
func (r RPCTopic) ProtocolPrefix() string {
	prefix, _, _, err := TopicDeconstructor(string(r))
	if err != nil {
		return ""
	}
	return prefix
}

// MessageType returns the message name component of the RPC topic.
func (r RPCTopic) MessageType() string {
	_, message, _, err := TopicDeconstructor(string(r))
	if err != nil {
		return ""
	}
	return message
}

// Version returns the schema version component of the RPC topic.
func (r RPCTopic) Version() string {
	_, _, version, err := TopicDeconstructor(string(r))
	if err != nil {
		return ""
	}
	return version
}

// TopicFromMessage constructs the RPC topic from a message name.
func TopicFromMessage(msg string) (string, error) {
	if !messageMapping[msg] {
		return "", errors.Errorf("provided message type doesn't have a registered mapping: %s", msg)
	}
	return protocolPrefix + msg + SchemaVersionV1, nil
}
