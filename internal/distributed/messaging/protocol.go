// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Envelope wire format for the messaging relay. EnvelopeVersion1
// layout carries sender, topic, payload, timestamp and signature,
// bounded by MaxEnvelopeSize and MaxTimestampDrift. Topics follow
// the ContentTopicFormat /n42/msg/1/{app}/{encoding}/{content}.

package messaging

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"golang.org/x/crypto/sha3"
)

const (
	// EnvelopeVersion1 is the current envelope wire format version.
	EnvelopeVersion1 = 1

	// MaxEnvelopeSize is the default maximum envelope size (256 KiB).
	MaxEnvelopeSize = 256 << 10

	// MaxTimestampDrift is the maximum allowed clock drift for envelope timestamps.
	MaxTimestampDrift = 20 * time.Second

	// ContentTopicFormat defines structured content topics.
	// Format: /n42/msg/1/{app}/{encoding}/{content-topic}
	ContentTopicFormat = "/n42/msg/1/%s/%s/%s"
)

// Envelope is a signed message container for P2P relay.
type Envelope struct {
	Version   uint8  `json:"version"`
	Sender    []byte `json:"sender"`    // compressed public key (33 bytes)
	Topic     string `json:"topic"`
	Payload   []byte `json:"payload"`
	Timestamp int64  `json:"timestamp"` // unix nanoseconds
	Nonce     uint64 `json:"nonce"`
	Signature []byte `json:"signature"` // secp256k1 signature (65 bytes)
}

// ContentTopic builds a structured content topic string.
func ContentTopic(app, encoding, topic string) string {
	return fmt.Sprintf(ContentTopicFormat, app, encoding, topic)
}

// EnvelopeID computes the unique ID for an envelope using Keccak256.
// Uses the same signing hash (everything except the signature).
func EnvelopeID(env *Envelope) types.Hash {
	var id types.Hash
	copy(id[:], envelopeSignHash(env))
	return id
}

// EncodeEnvelope serializes an envelope to bytes.
// Format: [version(1)] [senderLen(2)] [sender] [topicLen(2)] [topic]
//
//	[payloadLen(4)] [payload] [timestamp(8)] [nonce(8)] [sigLen(2)] [sig]
func EncodeEnvelope(env *Envelope) ([]byte, error) {
	if env == nil {
		return nil, errors.New("nil envelope")
	}
	size := 1 + 2 + len(env.Sender) + 2 + len(env.Topic) + 4 + len(env.Payload) + 8 + 8 + 2 + len(env.Signature)
	buf := make([]byte, 0, size)

	buf = append(buf, env.Version)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(env.Sender)))
	buf = append(buf, env.Sender...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(env.Topic)))
	buf = append(buf, []byte(env.Topic)...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(env.Payload)))
	buf = append(buf, env.Payload...)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(env.Timestamp))
	buf = append(buf, ts[:]...)
	var nonce [8]byte
	binary.BigEndian.PutUint64(nonce[:], env.Nonce)
	buf = append(buf, nonce[:]...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(env.Signature)))
	buf = append(buf, env.Signature...)

	return buf, nil
}

// DecodeEnvelope deserializes an envelope from bytes.
func DecodeEnvelope(data []byte) (*Envelope, error) {
	if len(data) < 1 {
		return nil, errors.New("envelope too short")
	}
	env := &Envelope{}
	pos := 0

	env.Version = data[pos]
	pos++

	if pos+2 > len(data) {
		return nil, errors.New("truncated sender length")
	}
	senderLen := int(binary.BigEndian.Uint16(data[pos:]))
	pos += 2
	if pos+senderLen > len(data) {
		return nil, errors.New("truncated sender")
	}
	env.Sender = make([]byte, senderLen)
	copy(env.Sender, data[pos:pos+senderLen])
	pos += senderLen

	if pos+2 > len(data) {
		return nil, errors.New("truncated topic length")
	}
	topicLen := int(binary.BigEndian.Uint16(data[pos:]))
	pos += 2
	if pos+topicLen > len(data) {
		return nil, errors.New("truncated topic")
	}
	env.Topic = string(data[pos : pos+topicLen])
	pos += topicLen

	if pos+4 > len(data) {
		return nil, errors.New("truncated payload length")
	}
	payloadLen := int(binary.BigEndian.Uint32(data[pos:]))
	pos += 4
	if pos+payloadLen > len(data) {
		return nil, errors.New("truncated payload")
	}
	env.Payload = make([]byte, payloadLen)
	copy(env.Payload, data[pos:pos+payloadLen])
	pos += payloadLen

	if pos+16 > len(data) {
		return nil, errors.New("truncated timestamp/nonce")
	}
	env.Timestamp = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8
	env.Nonce = binary.BigEndian.Uint64(data[pos:])
	pos += 8

	if pos+2 > len(data) {
		return nil, errors.New("truncated signature length")
	}
	sigLen := int(binary.BigEndian.Uint16(data[pos:]))
	pos += 2
	if pos+sigLen > len(data) {
		return nil, errors.New("truncated signature")
	}
	env.Signature = make([]byte, sigLen)
	copy(env.Signature, data[pos:pos+sigLen])

	return env, nil
}

// SignEnvelope signs an envelope with the given private key.
func SignEnvelope(env *Envelope, key *ecdsa.PrivateKey) error {
	env.Sender = crypto.CompressPubkey(&key.PublicKey)
	hash := envelopeSignHash(env)
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return fmt.Errorf("sign envelope: %w", err)
	}
	env.Signature = sig
	return nil
}

// ValidateEnvelope checks signature, timestamp, and size limits.
func ValidateEnvelope(env *Envelope, maxSize int) error {
	if env.Version != EnvelopeVersion1 {
		return fmt.Errorf("unsupported envelope version: %d", env.Version)
	}
	encoded, err := EncodeEnvelope(env)
	if err != nil {
		return fmt.Errorf("encode for size check: %w", err)
	}
	if len(encoded) > maxSize {
		return fmt.Errorf("envelope too large: %d > %d", len(encoded), maxSize)
	}

	// Check timestamp drift
	now := time.Now().UnixNano()
	drift := time.Duration(now - env.Timestamp)
	if drift < 0 {
		drift = -drift
	}
	if drift > MaxTimestampDrift {
		return fmt.Errorf("timestamp drift too large: %v", drift)
	}

	// Verify signature
	if len(env.Signature) != 65 {
		return fmt.Errorf("invalid signature length: %d", len(env.Signature))
	}
	hash := envelopeSignHash(env)
	pubkey, err := crypto.SigToPub(hash, env.Signature)
	if err != nil {
		return fmt.Errorf("recover pubkey: %w", err)
	}
	compressed := crypto.CompressPubkey(pubkey)
	if len(env.Sender) > 0 && !bytes.Equal(compressed, env.Sender) {
		return errors.New("sender mismatch")
	}

	return nil
}

// envelopeSignHash computes the hash to be signed (everything except signature).
func envelopeSignHash(env *Envelope) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte{env.Version})
	h.Write(env.Sender)
	h.Write([]byte(env.Topic))
	h.Write(env.Payload)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(env.Timestamp))
	h.Write(ts[:])
	var nonce [8]byte
	binary.BigEndian.PutUint64(nonce[:], env.Nonce)
	h.Write(nonce[:])
	return h.Sum(nil)
}
