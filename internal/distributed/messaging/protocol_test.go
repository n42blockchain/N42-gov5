package messaging

import (
	"bytes"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
)

func TestEnvelopeEncodeDecode(t *testing.T) {
	env := &Envelope{
		Version:   EnvelopeVersion1,
		Sender:    []byte("compressed-pubkey-33-bytes-xxxxx"),
		Topic:     "/n42/msg/1/chat/proto/messages",
		Payload:   []byte("hello world"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     12345,
		Signature: make([]byte, 65),
	}

	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}

	if decoded.Version != env.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, env.Version)
	}
	if !bytes.Equal(decoded.Sender, env.Sender) {
		t.Errorf("Sender mismatch")
	}
	if decoded.Topic != env.Topic {
		t.Errorf("Topic = %q, want %q", decoded.Topic, env.Topic)
	}
	if !bytes.Equal(decoded.Payload, env.Payload) {
		t.Errorf("Payload mismatch")
	}
	if decoded.Timestamp != env.Timestamp {
		t.Errorf("Timestamp = %d, want %d", decoded.Timestamp, env.Timestamp)
	}
	if decoded.Nonce != env.Nonce {
		t.Errorf("Nonce = %d, want %d", decoded.Nonce, env.Nonce)
	}
	if !bytes.Equal(decoded.Signature, env.Signature) {
		t.Errorf("Signature mismatch")
	}
}

func TestEnvelopeEncodeDecodeEmpty(t *testing.T) {
	env := &Envelope{
		Version:   EnvelopeVersion1,
		Sender:    nil,
		Topic:     "",
		Payload:   nil,
		Timestamp: 0,
		Nonce:     0,
		Signature: nil,
	}

	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope (empty): %v", err)
	}

	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope (empty): %v", err)
	}

	if decoded.Version != EnvelopeVersion1 {
		t.Errorf("Version = %d, want %d", decoded.Version, EnvelopeVersion1)
	}
	if len(decoded.Sender) != 0 {
		t.Errorf("Sender should be empty, got %d bytes", len(decoded.Sender))
	}
	if decoded.Topic != "" {
		t.Errorf("Topic = %q, want empty", decoded.Topic)
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("Payload should be empty, got %d bytes", len(decoded.Payload))
	}
	if decoded.Timestamp != 0 {
		t.Errorf("Timestamp = %d, want 0", decoded.Timestamp)
	}
	if decoded.Nonce != 0 {
		t.Errorf("Nonce = %d, want 0", decoded.Nonce)
	}
	if len(decoded.Signature) != 0 {
		t.Errorf("Signature should be empty, got %d bytes", len(decoded.Signature))
	}
}

func TestEnvelopeID(t *testing.T) {
	env1 := &Envelope{
		Version:   EnvelopeVersion1,
		Sender:    []byte("sender-a"),
		Topic:     "topic-a",
		Payload:   []byte("payload-a"),
		Timestamp: 1000,
		Nonce:     1,
	}

	// Same envelope produces same ID.
	id1a := EnvelopeID(env1)
	id1b := EnvelopeID(env1)
	if id1a != id1b {
		t.Fatal("same envelope should produce same ID")
	}

	// Different envelope produces different ID.
	env2 := &Envelope{
		Version:   EnvelopeVersion1,
		Sender:    []byte("sender-b"),
		Topic:     "topic-b",
		Payload:   []byte("payload-b"),
		Timestamp: 2000,
		Nonce:     2,
	}
	id2 := EnvelopeID(env2)
	if id1a == id2 {
		t.Fatal("different envelopes should produce different IDs")
	}

	// Changing just the nonce produces a different ID.
	env3 := &Envelope{
		Version:   EnvelopeVersion1,
		Sender:    []byte("sender-a"),
		Topic:     "topic-a",
		Payload:   []byte("payload-a"),
		Timestamp: 1000,
		Nonce:     99,
	}
	id3 := EnvelopeID(env3)
	if id1a == id3 {
		t.Fatal("changing nonce should change ID")
	}
}

func TestSignAndValidateEnvelope(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "test-topic",
		Payload:   []byte("test-payload"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     42,
	}

	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	if len(env.Signature) != 65 {
		t.Fatalf("Signature length = %d, want 65", len(env.Signature))
	}
	if len(env.Sender) != 33 {
		t.Fatalf("Sender length = %d, want 33 (compressed pubkey)", len(env.Sender))
	}

	if err := ValidateEnvelope(env, MaxEnvelopeSize); err != nil {
		t.Fatalf("ValidateEnvelope: %v", err)
	}
}

func TestValidateEnvelopeBadSignature(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "test-topic",
		Payload:   []byte("test-payload"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     1,
	}

	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	// Tamper with the signature.
	env.Signature[0] ^= 0xFF
	env.Signature[10] ^= 0xFF

	err = ValidateEnvelope(env, MaxEnvelopeSize)
	if err == nil {
		t.Fatal("expected validation to fail with tampered signature")
	}
}

func TestValidateEnvelopeTimestampDrift(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "test-topic",
		Payload:   []byte("test-payload"),
		Timestamp: time.Now().Add(-time.Hour).UnixNano(), // 1 hour in the past
		Nonce:     1,
	}

	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	err = ValidateEnvelope(env, MaxEnvelopeSize)
	if err == nil {
		t.Fatal("expected validation to fail with large timestamp drift")
	}
}

func TestValidateEnvelopeTooLarge(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "test-topic",
		Payload:   make([]byte, 1024), // 1KiB payload
		Timestamp: time.Now().UnixNano(),
		Nonce:     1,
	}

	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	// Set max size to something very small so envelope exceeds it.
	err = ValidateEnvelope(env, 100)
	if err == nil {
		t.Fatal("expected validation to fail with too-large envelope")
	}
}

func TestContentTopic(t *testing.T) {
	result := ContentTopic("chat", "proto", "messages")
	expected := "/n42/msg/1/chat/proto/messages"
	if result != expected {
		t.Errorf("ContentTopic = %q, want %q", result, expected)
	}

	result2 := ContentTopic("defi", "json", "swaps")
	expected2 := "/n42/msg/1/defi/json/swaps"
	if result2 != expected2 {
		t.Errorf("ContentTopic = %q, want %q", result2, expected2)
	}
}

func TestDecodeEnvelopeTruncated(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "test-topic",
		Payload:   []byte("test-payload"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     1,
	}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	// Test various truncation points.
	truncPoints := []struct {
		name string
		len  int
	}{
		{"empty", 0},
		{"version only", 1},
		{"partial sender length", 2},
		{"sender length no data", 3},
		{"mid-encoded", len(data) / 2},
		{"near end", len(data) - 1},
	}

	for _, tc := range truncPoints {
		t.Run(tc.name, func(t *testing.T) {
			if tc.len > len(data) {
				t.Skip("truncation point beyond data length")
			}
			_, err := DecodeEnvelope(data[:tc.len])
			if err == nil {
				t.Errorf("expected error for truncated data at %d bytes", tc.len)
			}
		})
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	envelopes := []*Envelope{
		{
			Version:   EnvelopeVersion1,
			Topic:     "topic-1",
			Payload:   []byte("payload-1"),
			Timestamp: time.Now().UnixNano(),
			Nonce:     100,
		},
		{
			Version:   EnvelopeVersion1,
			Topic:     "topic-2",
			Payload:   []byte("a longer payload with more data inside it"),
			Timestamp: time.Now().Add(-time.Millisecond).UnixNano(),
			Nonce:     200,
		},
		{
			Version:   EnvelopeVersion1,
			Topic:     "/n42/msg/1/app/proto/events",
			Payload:   make([]byte, 4096),
			Timestamp: time.Now().UnixNano(),
			Nonce:     300,
		},
	}

	for i, env := range envelopes {
		if err := SignEnvelope(env, key); err != nil {
			t.Fatalf("envelope %d: SignEnvelope: %v", i, err)
		}

		data, err := EncodeEnvelope(env)
		if err != nil {
			t.Fatalf("envelope %d: EncodeEnvelope: %v", i, err)
		}

		decoded, err := DecodeEnvelope(data)
		if err != nil {
			t.Fatalf("envelope %d: DecodeEnvelope: %v", i, err)
		}

		if decoded.Version != env.Version {
			t.Errorf("envelope %d: Version = %d, want %d", i, decoded.Version, env.Version)
		}
		if !bytes.Equal(decoded.Sender, env.Sender) {
			t.Errorf("envelope %d: Sender mismatch", i)
		}
		if decoded.Topic != env.Topic {
			t.Errorf("envelope %d: Topic = %q, want %q", i, decoded.Topic, env.Topic)
		}
		if !bytes.Equal(decoded.Payload, env.Payload) {
			t.Errorf("envelope %d: Payload mismatch", i)
		}
		if decoded.Timestamp != env.Timestamp {
			t.Errorf("envelope %d: Timestamp = %d, want %d", i, decoded.Timestamp, env.Timestamp)
		}
		if decoded.Nonce != env.Nonce {
			t.Errorf("envelope %d: Nonce = %d, want %d", i, decoded.Nonce, env.Nonce)
		}
		if !bytes.Equal(decoded.Signature, env.Signature) {
			t.Errorf("envelope %d: Signature mismatch", i)
		}

		// Validate the decoded envelope (must have valid sig + recent timestamp).
		if err := ValidateEnvelope(decoded, MaxEnvelopeSize); err != nil {
			t.Errorf("envelope %d: ValidateEnvelope on decoded: %v", i, err)
		}
	}
}
