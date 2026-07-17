// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bridges the native p2p.P2P interface to hotstuff.P2PDirectSender, the
// direct libp2p stream send/receive capability HotStuff's Rotor needs for
// leader-direct votes and single-hop proposal relay. Without this adapter,
// hotstuff.Service's type assertion to P2PDirectSender always fails (no
// concrete type in the codebase implements SendRawBytes), so Rotor silently
// falls back to full gossip broadcast for every message, regardless of
// whether its peer registry (see learnValidatorPeer in hotstuff/service.go)
// is populated.

package node

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// Compile-time check: *hotstuffP2PAdapter must satisfy hotstuff.P2PDirectSender,
// the whole point of this file. Catches signature drift in either package.
var _ hotstuff.P2PDirectSender = (*hotstuffP2PAdapter)(nil)

// rotorStreamDeadline bounds how long a single relay/direct-send stream may
// block on read or write, so a slow or unresponsive peer cannot leak a
// goroutine per stream.
const rotorStreamDeadline = 10 * time.Second

// hotstuffP2PAdapter wraps p2p.P2P and adds the three methods
// hotstuff.P2PDirectSender needs beyond P2PPublisher. p2p.P2P already has its
// own SetStreamHandler(topic string, handler network.StreamHandler) — used by
// the sync and messaging layers for their own wire protocols — so this type
// defines its own SetStreamHandler with hotstuff's (data []byte, from peer.ID)
// signature directly on the adapter, which shadows the embedded interface's
// method for callers holding a *hotstuffP2PAdapter (the embedded one remains
// reachable via .P2P.SetStreamHandler for any other caller).
type hotstuffP2PAdapter struct {
	p2p.P2P
}

// newHotstuffP2PAdapter wraps a p2p.P2P so it satisfies hotstuff.P2PDirectSender.
func newHotstuffP2PAdapter(svc p2p.P2P) *hotstuffP2PAdapter {
	return &hotstuffP2PAdapter{P2P: svc}
}

// SendRawBytes opens a new libp2p stream to pid on the given protocol
// (topic), writes data, and closes the write side so the remote handler's
// io.ReadAll sees EOF.
func (a *hotstuffP2PAdapter) SendRawBytes(ctx context.Context, data []byte, topic string, pid peer.ID) error {
	h := a.Host()
	if h == nil {
		return fmt.Errorf("hotstuff p2p adapter: host unavailable")
	}
	stream, err := h.NewStream(ctx, pid, protocol.ID(topic))
	if err != nil {
		return err
	}
	defer stream.Close()

	deadline := time.Now().Add(rotorStreamDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = stream.SetWriteDeadline(deadline)

	if _, err := stream.Write(data); err != nil {
		_ = stream.Reset()
		return err
	}
	return stream.CloseWrite()
}

// SetStreamHandler registers a protocol handler on the p2p host that reads a
// full stream body and hands (bytes, sender peer.ID) to handler — the shape
// Rotor's relay/direct-send path expects.
func (a *hotstuffP2PAdapter) SetStreamHandler(topic string, handler func(data []byte, from peer.ID)) {
	h := a.Host()
	if h == nil {
		return
	}
	h.SetStreamHandler(protocol.ID(topic), func(stream network.Stream) {
		defer stream.Close()
		_ = stream.SetReadDeadline(time.Now().Add(rotorStreamDeadline))
		data, err := readRotorStream(stream)
		if err != nil {
			_ = stream.Reset()
			return
		}
		handler(data, stream.Conn().RemotePeer())
	})
}

func readRotorStream(r io.Reader) ([]byte, error) {
	// Direct streams bypass GossipSub's WithMaxMessageSize gate but carry the
	// same snappy gossip envelope. Apply the same 1 MiB bound before allocating
	// the full body so a peer cannot hold many handlers on unbounded io.ReadAll.
	limit := int64(encoder.MaxGossipSize)
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("hotstuff p2p adapter: stream exceeds %d bytes", limit)
	}
	return data, nil
}

// ConnectedPeers returns the peer IDs of all currently connected peers, read
// directly from the libp2p host — Rotor uses this only for diagnostics today,
// but it's part of the P2PDirectSender contract.
func (a *hotstuffP2PAdapter) ConnectedPeers() []peer.ID {
	h := a.Host()
	if h == nil {
		return nil
	}
	return h.Network().Peers()
}
