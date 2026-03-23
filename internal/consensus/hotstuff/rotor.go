// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Rotor implements single-hop block propagation inspired by Solana's Alpenglow
// Rotor protocol. The leader sends the proposal directly to a small set of
// deterministically-selected relay nodes via libp2p streams. Each relay then
// forwards the proposal to its assigned subset of validators.
//
// Architecture:
//
//	Leader ──direct──► [Relay₁, Relay₂, ..., Relayₖ]
//	                       │         │            │
//	                       ▼         ▼            ▼
//	                   [V₁,V₂]   [V₃,V₄]    [V₅,V₆]
//
// All validators compute the same relay assignment deterministically from the
// view number, so relay nodes know their role without coordination.
//
// Fallback: if direct send fails for any relay, the message is broadcast via
// GossipSub to ensure liveness.
type Rotor struct {
	mu sync.RWMutex

	relayCount int  // number of relay nodes per view
	enabled    bool // master switch

	// Peer registry: maps validator address → libp2p peer.ID.
	// Populated by the service layer when validators connect.
	peerRegistry map[types.Address]peer.ID

	// Metrics.
	directSends  atomic.Int64
	relayedMsgs  atomic.Int64
	fallbackUsed atomic.Int64
}

// RelayAssignment maps a relay validator to its target validators.
type RelayAssignment struct {
	RelayIndex ValidatorIndex
	Targets    []ValidatorIndex
}

// DirectSender abstracts the ability to send data directly to a peer.
type DirectSender interface {
	// SendRawDirect sends raw bytes to a specific peer via a libp2p stream.
	// Returns nil on success. The implementation should handle stream lifecycle.
	SendRawDirect(ctx context.Context, data []byte, topic string, pid peer.ID) error
}

// NewRotor creates a new Rotor instance.
func NewRotor(relayCount int) *Rotor {
	if relayCount <= 0 {
		relayCount = 3
	}
	return &Rotor{
		relayCount:   relayCount,
		enabled:      true,
		peerRegistry: make(map[types.Address]peer.ID),
	}
}

// RegisterValidator associates a validator address with its libp2p peer ID.
// Called when a validator peer connects and identifies itself.
func (r *Rotor) RegisterValidator(addr types.Address, pid peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peerRegistry[addr] = pid
}

// UnregisterValidator removes a validator's peer mapping (e.g., on disconnect).
func (r *Rotor) UnregisterValidator(addr types.Address) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peerRegistry, addr)
}

// LookupPeer returns the peer ID for a validator index, if known.
func (r *Rotor) LookupPeer(vs *ValidatorSet, idx ValidatorIndex) (peer.ID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	addr, err := vs.GetAddress(idx)
	if err != nil {
		return "", false
	}
	pid, ok := r.peerRegistry[addr]
	return pid, ok
}

// RegisteredCount returns the number of validators with known peer IDs.
func (r *Rotor) RegisteredCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peerRegistry)
}

// SelectRelays deterministically selects relay nodes for a given view.
// All validators compute the same selection from SHA256(view), ensuring
// agreement without communication.
func (r *Rotor) SelectRelays(view ViewNumber, vs *ValidatorSet, leader ValidatorIndex) []RelayAssignment {
	nVal := int(vs.Len())
	if nVal <= 1 {
		return nil
	}

	relayCount := r.relayCount
	if relayCount >= nVal {
		relayCount = nVal - 1
	}

	// Deterministic seed from view number.
	var seed [8]byte
	binary.LittleEndian.PutUint64(seed[:], uint64(view))
	h := sha256.Sum256(seed[:])

	// Build candidate list (all validators except leader).
	candidates := make([]ValidatorIndex, 0, nVal-1)
	for i := 0; i < nVal; i++ {
		idx := ValidatorIndex(i)
		if idx != leader {
			candidates = append(candidates, idx)
		}
	}

	// Deterministic Fisher-Yates shuffle with pre-allocated buffer.
	var buf [36]byte // 32 (hash) + 4 (position)
	copy(buf[:32], h[:])
	for i := len(candidates) - 1; i > 0; i-- {
		binary.LittleEndian.PutUint32(buf[32:], uint32(i))
		posHash := sha256.Sum256(buf[:])
		j := int(binary.LittleEndian.Uint64(posHash[:8]) % uint64(i+1))
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}

	// Select relay nodes and assign remaining validators round-robin.
	relays := candidates[:relayCount]
	remaining := candidates[relayCount:]

	assignments := make([]RelayAssignment, relayCount)
	for i, relay := range relays {
		assignments[i] = RelayAssignment{
			RelayIndex: relay,
			Targets:    make([]ValidatorIndex, 0, len(remaining)/relayCount+1),
		}
	}
	for i, target := range remaining {
		assignments[i%relayCount].Targets = append(assignments[i%relayCount].Targets, target)
	}

	// Each relay also delivers to itself.
	for i := range assignments {
		assignments[i].Targets = append(assignments[i].Targets, assignments[i].RelayIndex)
	}

	sort.Slice(assignments, func(i, j int) bool {
		return assignments[i].RelayIndex < assignments[j].RelayIndex
	})

	return assignments
}

// BroadcastViaRelays sends a proposal through the Rotor relay network.
//
// The leader sends directly to each relay node via libp2p stream. If any relay
// send fails, the system falls back to GossipSub broadcast for reliability.
//
// When directSender is nil or no relay peers are registered, immediately falls
// back to gossip broadcast.
func (r *Rotor) BroadcastViaRelays(
	ctx context.Context,
	view ViewNumber,
	vs *ValidatorSet,
	leader ValidatorIndex,
	directSender DirectSender,
	directTopic string,
	gossipFallback func(data []byte) error,
	data []byte,
) error {
	r.mu.RLock()
	enabled := r.enabled
	r.mu.RUnlock()

	if !enabled || int(vs.Len()) <= 3 {
		return gossipFallback(data)
	}

	assignments := r.SelectRelays(view, vs, leader)
	if len(assignments) == 0 {
		return gossipFallback(data)
	}

	// If no direct sender or no registered peers, use gossip.
	if directSender == nil || r.RegisteredCount() == 0 {
		return gossipFallback(data)
	}

	log.Debug("rotor: sending to relay nodes",
		"view", view,
		"relays", len(assignments),
		"registeredPeers", r.RegisteredCount(),
	)

	// Send to each relay node directly. Track failures.
	var (
		sent   int
		failed int
	)

	sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	for _, assignment := range assignments {
		pid, ok := r.LookupPeer(vs, assignment.RelayIndex)
		if !ok {
			failed++
			continue
		}

		if err := directSender.SendRawDirect(sendCtx, data, directTopic, pid); err != nil {
			log.Debug("rotor: direct send to relay failed",
				"relay", assignment.RelayIndex, "peer", pid, "err", err)
			failed++
			continue
		}
		sent++
		r.directSends.Add(1)
	}

	// If we reached at least one relay, consider it a success.
	// Relays will forward to their targets. The leader also participates
	// in gossip as a safety net.
	if sent > 0 {
		log.Debug("rotor: relay send complete",
			"sent", sent, "failed", failed, "total", len(assignments))
		// Also broadcast via gossip as safety net.
		if err := gossipFallback(data); err != nil {
			log.Warn("rotor: gossip safety net failed", "err", err)
		}
		return nil
	}

	// All relay sends failed — full gossip fallback.
	r.fallbackUsed.Add(1)
	log.Warn("rotor: all relay sends failed, using gossip fallback",
		"view", view, "failed", failed)
	return gossipFallback(data)
}

// ForwardToTargets is called by relay nodes to forward a received proposal
// to their assigned target validators.
func (r *Rotor) ForwardToTargets(
	ctx context.Context,
	view ViewNumber,
	vs *ValidatorSet,
	leader ValidatorIndex,
	myIndex ValidatorIndex,
	directSender DirectSender,
	directTopic string,
	data []byte,
) {
	assignments := r.SelectRelays(view, vs, leader)

	// Find my assignment.
	var myAssignment *RelayAssignment
	for i := range assignments {
		if assignments[i].RelayIndex == myIndex {
			myAssignment = &assignments[i]
			break
		}
	}
	if myAssignment == nil {
		return // not a relay for this view
	}

	r.relayedMsgs.Add(1)

	sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	forwarded := 0
	for _, target := range myAssignment.Targets {
		if target == myIndex {
			continue // don't forward to self
		}
		pid, ok := r.LookupPeer(vs, target)
		if !ok {
			continue
		}
		if err := directSender.SendRawDirect(sendCtx, data, directTopic, pid); err != nil {
			log.Trace("rotor: relay forward failed",
				"target", target, "peer", pid, "err", err)
			continue
		}
		forwarded++
	}

	log.Debug("rotor: relay forwarding complete",
		"view", view, "relay", myIndex,
		"targets", len(myAssignment.Targets)-1, "forwarded", forwarded)
}

// IsRelay returns true if the given validator is a relay for the current view.
func (r *Rotor) IsRelay(view ViewNumber, vs *ValidatorSet, leader, myIndex ValidatorIndex) bool {
	assignments := r.SelectRelays(view, vs, leader)
	for _, a := range assignments {
		if a.RelayIndex == myIndex {
			return true
		}
	}
	return false
}

// SetEnabled enables or disables Rotor.
func (r *Rotor) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = enabled
}

// Enabled returns whether Rotor is active.
func (r *Rotor) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// Stats returns Rotor metrics.
func (r *Rotor) Stats() (directSends, relayed, fallbacks int64) {
	return r.directSends.Load(), r.relayedMsgs.Load(), r.fallbackUsed.Load()
}
