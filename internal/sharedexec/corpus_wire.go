package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/types"
)

const CorpusWireHeaderSize = 84
const CorpusMaxFramePayload = CorpusMaxFrameTxs * (4 + CorpusMaxTxBytes)

var corpusWireMagic = [4]byte{'G', 'C', 'F', '1'}

type CorpusFrameID struct {
	CorpusID types.Hash
	Shard    uint16
	Sequence uint64
}

// Payload aliases its reader buffer. Ownership must transfer or bytes must be
// copied before the next read. Neither a payload hash nor this format proves
// transaction authorization; the qualification sink reports crypto_verified=0.
type CorpusFrame struct {
	CorpusFrameID
	Transactions uint16
	PayloadHash  types.Hash
	Payload      []byte
}

func (f CorpusFrame) Header() ([CorpusWireHeaderSize]byte, error) {
	var header [CorpusWireHeaderSize]byte
	if f.CorpusID == (types.Hash{}) || f.Shard >= CorpusMaxShards || f.Transactions == 0 || f.Transactions > CorpusMaxFrameTxs || len(f.Payload) > CorpusMaxFramePayload || len(f.Payload) < int(f.Transactions)*5 {
		return header, errors.New("invalid bounded corpus frame")
	}
	copy(header[:4], corpusWireMagic[:])
	copy(header[4:36], f.CorpusID[:])
	binary.LittleEndian.PutUint16(header[36:38], f.Shard)
	binary.LittleEndian.PutUint64(header[38:46], f.Sequence)
	binary.LittleEndian.PutUint16(header[46:48], f.Transactions)
	binary.LittleEndian.PutUint32(header[48:52], uint32(len(f.Payload)))
	copy(header[52:84], f.PayloadHash[:])
	return header, nil
}

// WalkCorpusPayload checks exact entry lengths/counts without allocating a
// transaction object. Raw signed transaction bytes are never patched/repeated.
func WalkCorpusPayload(payload []byte, count uint16, visit func([]byte) error) error {
	if count == 0 || count > CorpusMaxFrameTxs || len(payload) > CorpusMaxFramePayload {
		return errors.New("invalid corpus payload count/size")
	}
	offset := 0
	for i := uint16(0); i < count; i++ {
		if len(payload)-offset < 4 {
			return io.ErrUnexpectedEOF
		}
		size := binary.LittleEndian.Uint32(payload[offset : offset+4])
		offset += 4
		if size == 0 || size > CorpusMaxTxBytes || uint64(size) > uint64(len(payload)-offset) {
			return errors.New("invalid corpus transaction byte length")
		}
		wire := payload[offset : offset+int(size)]
		offset += int(size)
		if visit != nil {
			if err := visit(wire); err != nil {
				return err
			}
		}
	}
	if offset != len(payload) {
		return errors.New("corpus payload has trailing entries")
	}
	return nil
}

func MakeCorpusFrame(id CorpusFrameID, transactions [][]byte) (CorpusFrame, error) {
	if len(transactions) == 0 || len(transactions) > CorpusMaxFrameTxs {
		return CorpusFrame{}, errors.New("invalid corpus frame transaction count")
	}
	size := 0
	for _, wire := range transactions {
		if len(wire) == 0 || len(wire) > CorpusMaxTxBytes {
			return CorpusFrame{}, errors.New("invalid corpus transaction size")
		}
		size += 4 + len(wire)
	}
	payload := make([]byte, 0, size)
	var prefix [4]byte
	for _, wire := range transactions {
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(wire)))
		payload = append(payload, prefix[:]...)
		payload = append(payload, wire...)
	}
	frame := CorpusFrame{CorpusFrameID: id, Transactions: uint16(len(transactions)), Payload: payload, PayloadHash: types.Hash(sha256.Sum256(payload))}
	if _, err := frame.Header(); err != nil {
		return CorpusFrame{}, err
	}
	return frame, nil
}

func (f CorpusFrame) Validate(descriptor CorpusDescriptor) error {
	id, err := descriptor.ID()
	if err != nil {
		return err
	}
	_, _, count, err := descriptor.FrameRange(f.Shard, f.Sequence)
	if err != nil {
		return err
	}
	if f.CorpusID != id || f.Transactions != count || types.Hash(sha256.Sum256(f.Payload)) != f.PayloadHash {
		return errors.New("corpus frame identity/sequence/count/digest mismatch")
	}
	return WalkCorpusPayload(f.Payload, f.Transactions, nil)
}

func WriteCorpusFrame(writer io.Writer, frame CorpusFrame) error {
	header, err := frame.Header()
	if err != nil {
		return err
	}
	if types.Hash(sha256.Sum256(frame.Payload)) != frame.PayloadHash {
		return errors.New("corpus payload hash mismatch")
	}
	if err := WalkCorpusPayload(frame.Payload, frame.Transactions, nil); err != nil {
		return err
	}
	if _, err := io.Copy(writer, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err = io.Copy(writer, bytes.NewReader(frame.Payload))
	return err
}

// ReadCorpusFrame bounds all hostile counts/lengths before sizing a buffer.
// scratch is reusable per connection/shard, never a whole-corpus allocation.
func ReadCorpusFrame(reader io.Reader, scratch []byte) (CorpusFrame, []byte, error) {
	var header [CorpusWireHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return CorpusFrame{}, scratch, err
	}
	if !bytes.Equal(header[:4], corpusWireMagic[:]) {
		return CorpusFrame{}, scratch, errors.New("unsupported corpus wire version")
	}
	var frame CorpusFrame
	copy(frame.CorpusID[:], header[4:36])
	frame.Shard = binary.LittleEndian.Uint16(header[36:38])
	frame.Sequence = binary.LittleEndian.Uint64(header[38:46])
	frame.Transactions = binary.LittleEndian.Uint16(header[46:48])
	length := binary.LittleEndian.Uint32(header[48:52])
	copy(frame.PayloadHash[:], header[52:84])
	if frame.CorpusID == (types.Hash{}) || frame.Shard >= CorpusMaxShards || frame.Transactions == 0 || frame.Transactions > CorpusMaxFrameTxs || length > CorpusMaxFramePayload || uint64(length) < uint64(frame.Transactions)*5 {
		return CorpusFrame{}, scratch, errors.New("invalid bounded corpus frame header")
	}
	if cap(scratch) < int(length) {
		scratch = make([]byte, int(length))
	} else {
		scratch = scratch[:int(length)]
	}
	if _, err := io.ReadFull(reader, scratch); err != nil {
		return CorpusFrame{}, scratch, err
	}
	frame.Payload = scratch
	if types.Hash(sha256.Sum256(scratch)) != frame.PayloadHash {
		return CorpusFrame{}, scratch, errors.New("corpus payload digest mismatch")
	}
	if err := WalkCorpusPayload(scratch, frame.Transactions, nil); err != nil {
		return CorpusFrame{}, scratch, err
	}
	return frame, scratch, nil
}
