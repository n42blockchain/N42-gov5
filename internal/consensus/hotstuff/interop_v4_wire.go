// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/golang/snappy"
	"github.com/n42blockchain/N42/common/types"
)

const h2V4MaxWireSize = 8192

type H2V4Envelope struct {
	Identity    H2V4ChainIdentity
	ChangesHash types.Hash
	Message     *ConsensusMsg
}

func EncodeH2V4Envelope(envelope H2V4Envelope) ([]byte, error) {
	if envelope.Message == nil {
		return nil, errors.New("nil H2-v4 message")
	}
	wire, err := EncodeConsensusMsg(envelope.Message)
	if err != nil {
		return nil, err
	}
	if len(wire) > h2V4MaxWireSize {
		return nil, fmt.Errorf("H2-v4 message too large: %d", len(wire))
	}
	out := make([]byte, 0, 7+8+32+32+4+len(wire))
	out = append(out, h2V4DomainPrefix[:]...)
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], envelope.Identity.ChainID)
	out = append(out, le[:]...)
	out = append(out, envelope.Identity.GenesisHash[:]...)
	out = append(out, envelope.ChangesHash[:]...)
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(wire)))
	out = append(out, size[:]...)
	return append(out, wire...), nil
}

func DecodeH2V4Envelope(data []byte, expected H2V4ChainIdentity) (*H2V4Envelope, error) {
	const headerSize = 7 + 8 + 32 + 32 + 4
	if len(data) < headerSize {
		return nil, errors.New("truncated H2-v4 envelope")
	}
	if !bytes.Equal(data[:7], h2V4DomainPrefix[:]) {
		return nil, errors.New("invalid H2-v4 magic")
	}
	identity := H2V4ChainIdentity{ChainID: binary.LittleEndian.Uint64(data[7:15])}
	copy(identity.GenesisHash[:], data[15:47])
	if identity != expected {
		return nil, errors.New("H2-v4 chain identity mismatch")
	}
	var changesHash types.Hash
	copy(changesHash[:], data[47:79])
	wireLen := int(binary.LittleEndian.Uint32(data[79:83]))
	if wireLen > h2V4MaxWireSize || wireLen != len(data)-headerSize {
		return nil, errors.New("invalid H2-v4 message length")
	}
	wire := data[headerSize:]
	msg, err := DecodeConsensusMsg(wire)
	if err != nil {
		return nil, err
	}
	canonical, err := EncodeConsensusMsg(msg)
	if err != nil || !bytes.Equal(canonical, wire) {
		return nil, errors.New("non-canonical H2-v4 payload")
	}
	return &H2V4Envelope{Identity: identity, ChangesHash: changesHash, Message: msg}, nil
}

func EncodeH2V4Gossip(envelope H2V4Envelope) ([]byte, error) {
	wire, err := EncodeH2V4Envelope(envelope)
	if err != nil {
		return nil, err
	}
	return snappy.Encode(nil, wire), nil
}

func DecodeH2V4Gossip(data []byte, expected H2V4ChainIdentity) (*H2V4Envelope, error) {
	decodedLen, err := snappy.DecodedLen(data)
	if err != nil || decodedLen > 83+h2V4MaxWireSize {
		return nil, errors.New("invalid H2-v4 snappy payload")
	}
	wire, err := snappy.Decode(nil, data)
	if err != nil {
		return nil, err
	}
	return DecodeH2V4Envelope(wire, expected)
}
