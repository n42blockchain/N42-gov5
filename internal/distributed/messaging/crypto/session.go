// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bilateral Session with chain-key ratcheting for forward secrecy.
// Derives a unique message key per SendCounter / RecvCounter, then
// ratchets SendChainKey and RecvChainKey forward so a compromise of
// the current state cannot decrypt past messages.

package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// maxRatchetSkip bounds how far ahead of RecvCounter a single frame may skip
// the receive chain in one Decrypt, so an attacker-supplied counter cannot
// force an unbounded pre-authentication ratchet loop.
const maxRatchetSkip = 10000

// Session represents a bilateral encrypted session with chain key ratcheting.
// Each message uses a unique message key derived from the chain key, providing
// forward secrecy: compromising the current state does not reveal past messages.
type Session struct {
	mu sync.Mutex

	// Identity keys
	LocalKey  [32]byte // our X25519 private key
	RemoteKey [32]byte // peer's X25519 public key

	// Chain key ratchet state
	SendChainKey [32]byte // current sending chain key
	RecvChainKey [32]byte // current receiving chain key

	// Message counters
	SendCounter uint64
	RecvCounter uint64

	// Session ID for serialization
	SessionID [32]byte
}

// InitiateSession creates a new session as the initiator.
// Performs X25519 ECDH and derives initial chain keys.
func InitiateSession(localPrivate [32]byte, remotePublic [32]byte) (*Session, error) {
	shared, err := curve25519.X25519(localPrivate[:], remotePublic[:])
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	localPub, err := curve25519.X25519(localPrivate[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("compute local public key: %w", err)
	}

	// Derive initial chain keys using HKDF
	// Use ordered concatenation of public keys as info to ensure both sides derive the same keys
	var info []byte
	if compareBytes(localPub, remotePublic[:]) < 0 {
		info = append(localPub, remotePublic[:]...)
	} else {
		info = append(remotePublic[:], localPub...)
	}

	hkdfReader := hkdf.New(sha256.New, shared, []byte("n42-session-init-v1"), info)

	s := &Session{
		LocalKey:  localPrivate,
		RemoteKey: remotePublic,
	}
	// Read 32 bytes for send chain key, 32 bytes for recv chain key, 32 for session ID
	if _, err := io.ReadFull(hkdfReader, s.SendChainKey[:]); err != nil {
		return nil, fmt.Errorf("derive send key: %w", err)
	}
	if _, err := io.ReadFull(hkdfReader, s.RecvChainKey[:]); err != nil {
		return nil, fmt.Errorf("derive recv key: %w", err)
	}
	if _, err := io.ReadFull(hkdfReader, s.SessionID[:]); err != nil {
		return nil, fmt.Errorf("derive session ID: %w", err)
	}

	// If we're the "higher" key, swap send/recv so both sides agree
	if compareBytes(localPub, remotePublic[:]) > 0 {
		s.SendChainKey, s.RecvChainKey = s.RecvChainKey, s.SendChainKey
	}

	return s, nil
}

// Encrypt encrypts plaintext using the current send chain key, then ratchets forward.
func (s *Session) Encrypt(plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Derive message key from chain key + counter
	msgKey := deriveMessageKey(s.SendChainKey, s.SendCounter)

	// Encrypt with XChaCha20-Poly1305
	aead, err := chacha20poly1305.NewX(msgKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Output format: [counter(8)][nonce(24)][ciphertext]
	var counterBuf [8]byte
	binary.BigEndian.PutUint64(counterBuf[:], s.SendCounter)

	ciphertext := aead.Seal(nil, nonce, plaintext, counterBuf[:])

	result := make([]byte, 0, 8+chacha20poly1305.NonceSizeX+len(ciphertext))
	result = append(result, counterBuf[:]...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)

	// Ratchet forward
	s.SendChainKey = ratchetChainKey(s.SendChainKey)
	s.SendCounter++

	return result, nil
}

// Decrypt decrypts ciphertext using the receive chain key.
func (s *Session) Decrypt(data []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(data) < 8+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, errors.New("ciphertext too short")
	}

	counter := binary.BigEndian.Uint64(data[:8])
	nonce := data[8 : 8+chacha20poly1305.NonceSizeX]
	ciphertext := data[8+chacha20poly1305.NonceSizeX:]

	// Bound the out-of-order skip BEFORE looping: counter is attacker-controlled
	// (read straight from the frame) and the ratchet runs ahead of AEAD
	// authentication, so a frame with counter=2^64-1 would spin ~1.8e19 hash
	// steps while holding s.mu and permanently wedge the session. A genuine gap
	// is small; reject anything beyond the window (the AEAD tag would fail such
	// a frame anyway — this just refuses to do the work first).
	if counter > s.RecvCounter && counter-s.RecvCounter > maxRatchetSkip {
		return nil, fmt.Errorf("message counter gap too large: %d", counter-s.RecvCounter)
	}

	// Handle out-of-order messages: ratchet recv chain to match counter
	chainKey := s.RecvChainKey
	for i := s.RecvCounter; i < counter; i++ {
		chainKey = ratchetChainKey(chainKey)
	}

	msgKey := deriveMessageKey(chainKey, counter)

	aead, err := chacha20poly1305.NewX(msgKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	var counterBuf [8]byte
	binary.BigEndian.PutUint64(counterBuf[:], counter)

	plaintext, err := aead.Open(nil, nonce, ciphertext, counterBuf[:])
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	// Update recv state
	if counter >= s.RecvCounter {
		s.RecvChainKey = ratchetChainKey(chainKey)
		s.RecvCounter = counter + 1
	}

	return plaintext, nil
}

// SessionState is a serializable snapshot of a session for persistence.
type SessionState struct {
	SessionID    [32]byte
	LocalKey     [32]byte
	RemoteKey    [32]byte
	SendChainKey [32]byte
	RecvChainKey [32]byte
	SendCounter  uint64
	RecvCounter  uint64
}

// Export returns a serializable session state.
func (s *Session) Export() *SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &SessionState{
		SessionID:    s.SessionID,
		LocalKey:     s.LocalKey,
		RemoteKey:    s.RemoteKey,
		SendChainKey: s.SendChainKey,
		RecvChainKey: s.RecvChainKey,
		SendCounter:  s.SendCounter,
		RecvCounter:  s.RecvCounter,
	}
}

// ImportSession restores a session from a serialized state.
func ImportSession(state *SessionState) *Session {
	return &Session{
		SessionID:    state.SessionID,
		LocalKey:     state.LocalKey,
		RemoteKey:    state.RemoteKey,
		SendChainKey: state.SendChainKey,
		RecvChainKey: state.RecvChainKey,
		SendCounter:  state.SendCounter,
		RecvCounter:  state.RecvCounter,
	}
}

// deriveMessageKey derives a per-message key from chain key and counter.
func deriveMessageKey(chainKey [32]byte, counter uint64) [32]byte {
	h := sha256.New()
	h.Write(chainKey[:])
	h.Write([]byte("msg-key"))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	h.Write(buf[:])
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// ratchetChainKey advances the chain key forward by one step.
func ratchetChainKey(chainKey [32]byte) [32]byte {
	h := sha256.New()
	h.Write(chainKey[:])
	h.Write([]byte("chain-ratchet"))
	var next [32]byte
	copy(next[:], h.Sum(nil))
	return next
}

// compareBytes compares two byte slices lexicographically.
func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
