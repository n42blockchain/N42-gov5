package encoder

import (
	"io"

	ssz "github.com/prysmaticlabs/fastssz"
)

// NetworkEncoding defines the interface for P2P network message encoding,
// compatible with Ethereum consensus layer protocols.
type NetworkEncoding interface {
	// DecodeGossip decompresses and unmarshals a gossip message into dst.
	DecodeGossip([]byte, ssz.Unmarshaler) error
	// DecodeWithMaxLength reads a varint-prefixed message from r and
	// unmarshals it into dst, rejecting messages that exceed MaxChunkSize.
	DecodeWithMaxLength(io.Reader, ssz.Unmarshaler) error
	// EncodeGossip serializes and compresses msg, then writes it to w.
	EncodeGossip(io.Writer, ssz.Marshaler) (int, error)
	// EncodeWithMaxLength serializes msg with a varint length prefix and
	// writes the compressed result to w, rejecting messages that exceed MaxChunkSize.
	EncodeWithMaxLength(io.Writer, ssz.Marshaler) (int, error)
	// ProtocolSuffix returns the encoding scheme suffix for protocol IDs.
	ProtocolSuffix() string
}
