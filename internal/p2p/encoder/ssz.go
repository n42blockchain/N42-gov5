package encoder

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/golang/snappy"
	fastssz "github.com/prysmaticlabs/fastssz"
)

var _ NetworkEncoding = (*SszNetworkEncoder)(nil)

// MaxGossipSize is the maximum allowed size for gossip messages (1 MiB by
// default). It caps the practical block size: a block whose encoded gossip
// message exceeds it cannot propagate, so validators never import it, never
// vote, and the chain livelocks. N42_MAX_GOSSIP_MB raises it for high-gas-limit
// throughput testing — every node in the network MUST use the same value.
var MaxGossipSize = sizeFromEnvMB("N42_MAX_GOSSIP_MB", 1<<20)

// MaxChunkSize is the maximum allowed size for a single chunk (1 MiB by
// default). Raised together with MaxGossipSize: the block push stream framing
// uses it, so a large block fails to decode ("failed to read varint: EOF")
// if only the gossip cap is raised.
var MaxChunkSize = sizeFromEnvMB("N42_MAX_GOSSIP_MB", 1<<20)

// MaxWireMessageSize returns the largest payload the p2p layer can carry on
// EITHER delivery path: gossip (EncodeGossip, bounded by MaxGossipSize) or a
// direct libp2p stream (EncodeWithMaxLength, bounded by MaxChunkSize). Both
// bounds are checked against the UNCOMPRESSED serialized bytes — snappy is
// applied after the check — so producers must size their payload before
// compression.
//
// Block producers derive their packing budget from this so a sealed block is
// never too large to propagate (an unpropagatable block livelocks HotStuff:
// followers cannot import it, import-gated voting never fires, no quorum ever
// forms). Raising N42_MAX_GOSSIP_MB automatically raises the packing budget.
func MaxWireMessageSize() uint64 {
	if MaxChunkSize < MaxGossipSize {
		return MaxChunkSize
	}
	return MaxGossipSize
}

// MaxGossipWireSize is the largest number of bytes a legal gossip message can
// occupy ON THE WIRE, i.e. the cap the pubsub router must be configured with.
//
// It is NOT MaxGossipSize. EncodeGossip bounds the UNCOMPRESSED payload and then
// snappy-encodes it, and snappy expands incompressible input, so a payload that
// is legal by MaxGossipSize can serialize to more bytes than that. Configuring
// the router at MaxGossipSize would make the router drop messages this encoder
// considers valid. Widening it costs nothing in safety: DecodeGossip still
// rejects anything whose DECODED size exceeds MaxGossipSize.
func MaxGossipWireSize() uint64 {
	if n := snappy.MaxEncodedLen(int(MaxGossipSize)); n > 0 {
		return uint64(n)
	}
	// snappy.MaxEncodedLen returns -1 once the source exceeds ~3.7 GiB. Passing
	// that through would wrap to a negative router cap and silently reject every
	// gossip message — the exact failure this bound exists to prevent. Reproduce
	// snappy's formula in uint64 instead, where no realistic value can overflow.
	return 32 + MaxGossipSize + MaxGossipSize/6
}

// sizeFromEnvMB reads a size in MiB from env, falling back to def bytes when
// unset or invalid.
func sizeFromEnvMB(name string, def uint64) uint64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil && n > 0 {
			return n << 20
		}
	}
	return def
}

var bufWriterPool = new(sync.Pool)
var bufReaderPool = new(sync.Pool)

// SszNetworkEncoder supports p2p networking encoding using SSZ
// with snappy compression.
type SszNetworkEncoder struct{}

// ProtocolSuffixSSZSnappy is the suffix appended to protocol IDs to identify SSZ+snappy encoding.
const ProtocolSuffixSSZSnappy = "ssz_snappy"

// EncodeGossip serializes msg via SSZ, validates the size, compresses with
// snappy, and writes the result to w.
func (SszNetworkEncoder) EncodeGossip(w io.Writer, msg fastssz.Marshaler) (int, error) {
	if msg == nil {
		return 0, nil
	}
	b, err := msg.MarshalSSZ()
	if err != nil {
		return 0, err
	}
	if uint64(len(b)) > MaxGossipSize {
		return 0, fmt.Errorf("gossip message exceeds max gossip size: %d bytes > %d bytes", len(b), MaxGossipSize)
	}
	b = snappy.Encode(nil, b)
	return w.Write(b)
}

// EncodeWithMaxLength serializes msg via SSZ, prefixes the stream with a
// varint-encoded length, and writes snappy-compressed data to w.
// Returns an error if the serialized message exceeds MaxChunkSize.
func (SszNetworkEncoder) EncodeWithMaxLength(w io.Writer, msg fastssz.Marshaler) (int, error) {
	if msg == nil {
		return 0, nil
	}
	b, err := msg.MarshalSSZ()
	if err != nil {
		return 0, err
	}
	if uint64(len(b)) > MaxChunkSize {
		return 0, fmt.Errorf("encoded message size %d exceeds max chunk size %d", len(b), MaxChunkSize)
	}
	if _, err = w.Write(EncodeVarint(uint64(len(b)))); err != nil {
		return 0, err
	}
	return writeSnappyBuffer(w, b)
}

// DecodeGossip decompresses a snappy-encoded gossip message and unmarshals
// the result via SSZ into dst.
func (SszNetworkEncoder) DecodeGossip(b []byte, dst fastssz.Unmarshaler) error {
	decoded, err := DecodeSnappy(b, MaxGossipSize)
	if err != nil {
		return err
	}
	return dst.UnmarshalSSZ(decoded)
}

// DecodeSnappy decompresses a snappy message, returning an error if the
// decoded size exceeds maxSize.
func DecodeSnappy(msg []byte, maxSize uint64) ([]byte, error) {
	size, err := snappy.DecodedLen(msg)
	if err != nil {
		return nil, err
	}
	if uint64(size) > maxSize {
		return nil, fmt.Errorf("snappy message exceeds max size: %d bytes > %d bytes", size, maxSize)
	}
	return snappy.Decode(nil, msg)
}

// DecodeWithMaxLength reads a varint length prefix from r, then reads and
// decompresses the snappy-encoded payload, and unmarshals it via SSZ into dst.
// Returns an error if the declared message length exceeds MaxChunkSize.
func (e SszNetworkEncoder) DecodeWithMaxLength(r io.Reader, dst fastssz.Unmarshaler) error {
	msgLen, err := readVarint(r)
	if err != nil {
		return fmt.Errorf("failed to read varint: %w", err)
	}
	if msgLen > MaxChunkSize {
		return fmt.Errorf("message length %d exceeds max chunk size %d", msgLen, MaxChunkSize)
	}
	if msgLen > uint64(math.MaxInt) {
		return fmt.Errorf("message length %d exceeds maximum int value", msgLen)
	}

	msgMax, err := e.MaxLength(msgLen)
	if err != nil {
		return err
	}

	snappyReader := newBufferedReader(io.LimitReader(r, int64(msgMax)))
	defer bufReaderPool.Put(snappyReader)

	buf := make([]byte, int(msgLen))
	if _, err = io.ReadFull(snappyReader, buf); err != nil {
		return fmt.Errorf("failed to read snappy data (expected %d bytes, msgMax=%d): %w", msgLen, msgMax, err)
	}
	if err := dst.UnmarshalSSZ(buf); err != nil {
		return fmt.Errorf("SSZ unmarshal failed (type=%T, bufLen=%d): %w", dst, len(buf), err)
	}
	return nil
}

// ProtocolSuffix returns the protocol ID suffix for SSZ+snappy encoding.
func (SszNetworkEncoder) ProtocolSuffix() string {
	return "/" + ProtocolSuffixSSZSnappy
}

// MaxLength returns the maximum possible snappy-encoded length for the given
// uncompressed payload size.
func (SszNetworkEncoder) MaxLength(length uint64) (int, error) {
	if length > uint64(math.MaxInt) {
		return 0, fmt.Errorf("length %d exceeds maximum int value", length)
	}
	maxLen := snappy.MaxEncodedLen(int(length))
	if maxLen < 0 {
		return 0, fmt.Errorf("max encoded length is negative: %d", maxLen)
	}
	return maxLen, nil
}

// writeSnappyBuffer writes b through a pooled snappy buffered writer.
func writeSnappyBuffer(w io.Writer, b []byte) (int, error) {
	bufWriter := newBufferedWriter(w)
	defer bufWriterPool.Put(bufWriter)

	num, err := bufWriter.Write(b)
	if err != nil {
		_ = bufWriter.Close()
		return 0, err
	}
	return num, bufWriter.Close()
}

// newBufferedReader returns a snappy.Reader from the pool, or creates a new one.
func newBufferedReader(r io.Reader) *snappy.Reader {
	if v := bufReaderPool.Get(); v != nil {
		if reader, ok := v.(*snappy.Reader); ok {
			reader.Reset(r)
			return reader
		}
	}
	return snappy.NewReader(r)
}

// newBufferedWriter returns a snappy.Writer from the pool, or creates a new one.
func newBufferedWriter(w io.Writer) *snappy.Writer {
	if v := bufWriterPool.Get(); v != nil {
		if writer, ok := v.(*snappy.Writer); ok {
			writer.Reset(w)
			return writer
		}
	}
	return snappy.NewBufferedWriter(w)
}
