package sync

import (
	"fmt"
	"io"
	"sync/atomic"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	types "github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/proto/types_pb"
)

// chunkBlockWriter writes the given block as a chunked response to the stream.
// response_chunk  ::= <result> | <context-bytes> | <encoding-dependent-header> | <encoded-payload>
func (s *Service) chunkBlockWriter(stream libp2pcore.Stream, blk types.IBlock) error {
	SetStreamWriteDeadline(stream, defaultWriteDuration)
	return WriteBlockChunk(stream, s.cfg.chain, blk)
}

// WriteBlockChunk writes a block chunk object to the stream.
// response_chunk  ::= <result> | <context-bytes> | <encoding-dependent-header> | <encoded-payload>
func WriteBlockChunk(stream libp2pcore.Stream, chain common.IBlockChain, blk types.IBlock) error {
	return writeBlockChunk(stream, chain.GenesisBlock().Hash(), blk)
}

// writeBlockChunk is WriteBlockChunk over a plain io.Writer, so the wire format
// can be exercised without a libp2p stream.
func writeBlockChunk(w io.Writer, genesisHash comtypes.Hash, blk types.IBlock) error {
	// Encode the block as ETH-standard RLP (stage 1 of the proto->RLP migration).
	// The block hash is keccak(rlp(header)) and the header round-trips
	// byte-identically (see Block.EncodeRLP / Header rlp:"optional" tags), so the
	// receiver recomputes the identical hash. The bytes travel through the
	// existing length/snappy framing via rawSSZBytes.
	//
	// Everything that can fail is done before the first byte reaches the stream.
	// The chunk header (result code + context bytes) is only meaningful when a
	// payload follows it: a responder that writes the header and then fails to
	// encode leaves the requester holding five valid-looking bytes followed by
	// nothing, which it can only report as corrupt input.
	data, err := rlp.EncodeToBytes(blk)
	if err != nil {
		return err
	}
	// The cap has to be checked here rather than left to the encoder, which
	// checks it only after the caller has already committed the chunk header
	// to the stream. Blocks are the one payload that routinely runs into it.
	if uint64(len(data)) > encoder.MaxBlockChunkSize {
		return fmt.Errorf("block #%d encodes to %d bytes, over the %d byte chunk cap",
			blk.Number64().Uint64(), len(data), encoder.MaxBlockChunkSize)
	}

	digest, err := utils.CreateForkDigest(blk.Number64(), genesisHash)
	if err != nil {
		return err
	}

	if _, err = w.Write([]byte{responseCodeSuccess}); err != nil {
		return err
	}
	if _, err = w.Write(digest[:]); err != nil {
		return err
	}

	// MaxBlockChunkSize, not the default MaxChunkSize: this is the block-only
	// cap the requester decodes with (see readFirstChunkedBlock), and blocks
	// routinely exceed the 1 MiB default. Encoding with the smaller cap made
	// every catch-up request for a large block fail, so a node that fell behind
	// across such blocks could never rejoin -- while direct push, which already
	// used the block cap, kept the chain itself running.
	_, err = encoder.EncodeWithMaxLengthLimit(w, &rawSSZBytes{data: data}, encoder.MaxBlockChunkSize)
	return err
}

// rawSSZBytes carries arbitrary bytes through the SSZ length/snappy framing of
// EncodeWithMaxLength/DecodeWithMaxLength without imposing any SSZ schema, so we
// can ship proto-encoded blocks (which keep every header field and an
// arbitrary-length Extra, unlike the generated SSZ schema).
type rawSSZBytes struct{ data []byte }

func (r *rawSSZBytes) MarshalSSZ() ([]byte, error)             { return r.data, nil }
func (r *rawSSZBytes) MarshalSSZTo(buf []byte) ([]byte, error) { return append(buf, r.data...), nil }
func (r *rawSSZBytes) SizeSSZ() int                            { return len(r.data) }
func (r *rawSSZBytes) UnmarshalSSZ(buf []byte) error {
	r.data = append([]byte(nil), buf...)
	return nil
}

// legacyProtoBlocks gates the protobuf block fallback below. It is off unless a
// chain has been shown incapable of losing data through it; see
// AllowLegacyProtoBlocks.
var legacyProtoBlocks atomic.Bool

// AllowLegacyProtoBlocks enables the pre-RLP protobuf fallback in
// decodeChunkedBlock, but only for chains where reconstructing a block through
// types_pb cannot change its hash.
//
// types_pb.Header has no field for BlockAccessListHash (EIP-7928) or
// MobileRegistryRoot, and both are part of the header's RLP hash preimage. A
// block rebuilt from protobuf therefore hashes differently from the one the
// network agreed on the moment either field is in use -- and a legacy peer
// cannot supply them, because its build predates them. Header.Marshal carries
// them in a trailer alongside the protobuf body for exactly this reason;
// ToProtoMessage/FromProtoMessage alone do not.
//
// So the fallback is sound only where neither fork is configured at all, not
// merely where it has yet to activate: a chain that will switch them on later
// would silently start losing the field at the fork. Being wrong in the other
// direction only costs compatibility with peers that predate RLP, which is why
// the default is off.
func AllowLegacyProtoBlocks(cfg *params.ChainConfig) {
	legacyProtoBlocks.Store(cfg != nil && cfg.BALTime == nil && cfg.MobileAnchorTime == nil)
}

// decodeChunkedBlock accepts both generations of the block-range wire payload.
// New peers send ETH-standard RLP; deployed legacy-mainnet peers still send a
// protobuf Block inside the same length/snappy envelope. Keep the fallback on
// reads only so upgraded peers converge on RLP without stranding old nodes, and
// only where AllowLegacyProtoBlocks has established it cannot drop a field the
// block hash commits to.
func decodeChunkedBlock(data []byte) (*types.Block, error) {
	blk := new(types.Block)
	if err := rlp.DecodeBytes(data, blk); err == nil {
		return blk, nil
	} else {
		rlpErr := err
		if !legacyProtoBlocks.Load() {
			return nil, fmt.Errorf("RLP decode failed and the legacy protobuf fallback is "+
				"disabled on this chain (it cannot carry every header field the block hash "+
				"commits to): %w", rlpErr)
		}
		legacy := new(types_pb.Block)
		if err := proto.Unmarshal(data, legacy); err != nil {
			return nil, fmt.Errorf("RLP decode failed: %v; legacy protobuf decode failed: %w", rlpErr, err)
		}
		if err := blk.FromProtoMessage(legacy); err != nil {
			return nil, fmt.Errorf("RLP decode failed: %v; legacy protobuf conversion failed: %w", rlpErr, err)
		}
		return blk, nil
	}
}

// ReadChunkedBlock handles each response chunk that is sent by the peer and
// converts it into a block. The first chunk has different deadline handling.
func ReadChunkedBlock(stream libp2pcore.Stream, p2p p2p.EncodingProvider, isFirstChunk bool) (*types.Block, error) {
	if isFirstChunk {
		return readFirstChunkedBlock(stream, p2p)
	}
	return readResponseChunk(stream, p2p)
}

// readFirstChunkedBlock reads the first chunked block with appropriate deadlines.
func readFirstChunkedBlock(stream libp2pcore.Stream, p2p p2p.EncodingProvider) (*types.Block, error) {
	code, errMsg, err := ReadStatusCode(stream, p2p.Encoding())
	if err != nil {
		return nil, errors.Wrap(err, "failed to read status code from first chunk")
	}
	if code != 0 {
		return nil, fmt.Errorf("remote returned error code %d: %s", code, errMsg)
	}

	ctx, err := readContextFromStream(stream)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read context from stream")
	}
	log.Debug("First chunk context", "forkDigest", fmt.Sprintf("%x", ctx), "peer", stream.Conn().RemotePeer().String())

	raw := &rawSSZBytes{}
	if err = encoder.DecodeWithMaxLengthLimit(stream, raw, encoder.MaxBlockChunkSize); err != nil {
		return nil, errors.Wrapf(err, "failed to decode block from first chunk (forkDigest=%x)", ctx)
	}
	blk, err := decodeChunkedBlock(raw.data)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to decode block payload from first chunk (forkDigest=%x)", ctx)
	}
	log.Debug("First chunk decoded successfully", "blockNumber", blk.Number64().Uint64(), "peer", stream.Conn().RemotePeer().String())
	return blk, nil
}

// readResponseChunk reads a subsequent response chunk from the stream.
func readResponseChunk(stream libp2pcore.Stream, p2p p2p.EncodingProvider) (*types.Block, error) {
	SetStreamReadDeadline(stream, respTimeout)

	// Read status code (1 byte). Use stack-allocated array to avoid
	// heap allocation on every chunk in a range response.
	var statusBuf [1]byte
	n, err := io.ReadFull(stream, statusBuf[:])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, errors.Wrapf(err, "failed to read status code (read %d bytes)", n)
	}

	code := statusBuf[0]
	if code != 0 {
		msg := &p2ptypes.ErrorMessage{}
		if decErr := p2p.Encoding().DecodeWithMaxLength(stream, msg); decErr != nil {
			return nil, errors.Errorf("remote returned error code %d (failed to decode message: %v)", code, decErr)
		}
		return nil, errors.Errorf("remote returned error code %d: %s", code, string(*msg))
	}

	// Read fork digest (4 bytes). Stack-allocated to avoid per-chunk heap allocation.
	var forkDigest [forkDigestLength]byte
	n, err = io.ReadFull(stream, forkDigest[:])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			log.Debug("Stream ended after status code", "peer", stream.Conn().RemotePeer().String())
			return nil, io.EOF
		}
		return nil, errors.Wrapf(err, "failed to read fork digest (read %d bytes)", n)
	}
	log.Debug("Received chunk context", "forkDigest", fmt.Sprintf("%x", forkDigest), "peer", stream.Conn().RemotePeer().String())

	raw := &rawSSZBytes{}
	if err = encoder.DecodeWithMaxLengthLimit(stream, raw, encoder.MaxBlockChunkSize); err != nil {
		return nil, errors.Wrapf(err, "failed to decode block from chunk (forkDigest=%x)", forkDigest)
	}
	blk, err := decodeChunkedBlock(raw.data)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to decode block payload from chunk (forkDigest=%x)", forkDigest)
	}
	return blk, nil
}
