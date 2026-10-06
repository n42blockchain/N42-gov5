// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package v5wire

import (
	"context"
	"crypto/ecdsa"
	"testing"

	"github.com/n42blockchain/N42/common/mclock"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// hwPeer bundles together the local node and codec for one side of a
// handshake test.
type hwPeer struct {
	key   *ecdsa.PrivateKey
	ln    *enode.LocalNode
	codec *Codec
}

func newHWPeer(t *testing.T, clock mclock.Clock) *hwPeer {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	db, err := enode.OpenDB(context.Background(), "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(db.Close)
	ln := enode.NewLocalNode(db, key)
	return &hwPeer{
		key:   key,
		ln:    ln,
		codec: NewCodec(ln, key, clock, nil),
	}
}

// TestCodecHandshakeRoundTrip drives a full discv5 handshake between two
// codecs: random packet -> WHOAREYOU -> handshake message, then verifies a
// subsequent message is encoded/decoded using the established session keys.
func TestCodecHandshakeRoundTrip(t *testing.T) {
	clock := new(mclock.Simulated)

	initiator := newHWPeer(t, clock)
	recipient := newHWPeer(t, clock)

	initiatorAddr := "127.0.0.1:30303"
	recipientAddr := "127.0.0.1:30304"

	// Step 1: initiator sends a random packet to kick off the handshake
	// (no session yet, no challenge).
	ping := &Ping{ReqID: []byte{1}}
	packet1, _, err := initiator.codec.Encode(recipient.ln.ID(), recipientAddr, ping, nil)
	if err != nil {
		t.Fatalf("initiator encode random: %v", err)
	}

	// Step 2: recipient decodes the random packet. It cannot decrypt it
	// (no session), so it should come back as *Unknown.
	_, _, p, err := recipient.codec.Decode(packet1, initiatorAddr)
	if err != nil {
		t.Fatalf("recipient decode random packet: %v", err)
	}
	unknown, ok := p.(*Unknown)
	if !ok {
		t.Fatalf("expected *Unknown, got %T", p)
	}

	// Step 3: recipient responds with WHOAREYOU using the nonce it saw.
	challenge := &Whoareyou{
		Nonce:     unknown.Nonce,
		IDNonce:   [16]byte{1, 2, 3},
		RecordSeq: 0,
		Node:      initiator.ln.Node(),
	}
	waPacket, _, err := recipient.codec.Encode(initiator.ln.ID(), initiatorAddr, challenge, nil)
	if err != nil {
		t.Fatalf("recipient encode whoareyou: %v", err)
	}

	// Step 4: initiator decodes the WHOAREYOU.
	_, _, p2, err := initiator.codec.Decode(waPacket, recipientAddr)
	if err != nil {
		t.Fatalf("initiator decode whoareyou: %v", err)
	}
	gotChallenge, ok := p2.(*Whoareyou)
	if !ok {
		t.Fatalf("expected *Whoareyou, got %T", p2)
	}
	gotChallenge.Node = recipient.ln.Node()

	// Step 5: initiator sends a handshake message using the challenge.
	ping2 := &Ping{ReqID: []byte{2}}
	hsPacket, _, err := initiator.codec.Encode(recipient.ln.ID(), recipientAddr, ping2, gotChallenge)
	if err != nil {
		t.Fatalf("initiator encode handshake message: %v", err)
	}

	// Step 6: recipient decodes the handshake message, establishing a
	// session on both ends.
	fromID, fromNode, p3, err := recipient.codec.Decode(hsPacket, initiatorAddr)
	if err != nil {
		t.Fatalf("recipient decode handshake message: %v", err)
	}
	if fromID != initiator.ln.ID() {
		t.Fatalf("fromID = %v, want %v", fromID, initiator.ln.ID())
	}
	if fromNode == nil {
		t.Fatal("expected node record from handshake")
	}
	gotPing, ok := p3.(*Ping)
	if !ok {
		t.Fatalf("expected *Ping, got %T", p3)
	}
	if string(gotPing.ReqID) != string(ping2.ReqID) {
		t.Fatalf("ReqID = %x, want %x", gotPing.ReqID, ping2.ReqID)
	}

	// Step 7: now that both sides hold a session, plain messages should
	// round-trip without a further handshake.
	pong := &Pong{ReqID: []byte{3}, ToIP: nil, ToPort: 0}
	plainPacket, _, err := recipient.codec.Encode(initiator.ln.ID(), initiatorAddr, pong, nil)
	if err != nil {
		t.Fatalf("recipient encode plain message: %v", err)
	}
	_, _, p4, err := initiator.codec.Decode(plainPacket, recipientAddr)
	if err != nil {
		t.Fatalf("initiator decode plain message: %v", err)
	}
	gotPong, ok := p4.(*Pong)
	if !ok {
		t.Fatalf("expected *Pong, got %T", p4)
	}
	if string(gotPong.ReqID) != string(pong.ReqID) {
		t.Fatalf("ReqID = %x, want %x", gotPong.ReqID, pong.ReqID)
	}
}

// TestCodecDecodeRejectsMalformed exercises the decoder's rejection paths
// for malformed input.
func TestCodecDecodeRejectsMalformed(t *testing.T) {
	clock := new(mclock.Simulated)
	peer := newHWPeer(t, clock)

	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"too-short", make([]byte, 10)},
		{"garbage-full-size", make([]byte, 200)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := peer.codec.Decode(tt.data, "127.0.0.1:30303")
			if err == nil {
				t.Fatal("expected error decoding malformed packet")
			}
		})
	}
}

// TestCodecEncodeRejectsBadDestination checks that encoding a WHOAREYOU
// response without the destination node set fails with errMissingNode,
// and that a handshake header without a challenge node fails too.
func TestCodecEncodeRejectsBadDestination(t *testing.T) {
	clock := new(mclock.Simulated)
	peer := newHWPeer(t, clock)

	_, _, err := peer.codec.Encode(enode.ID{}, "addr", &Whoareyou{RecordSeq: 1}, nil)
	if err == nil {
		t.Fatal("expected error encoding whoareyou without node")
	}

	_, _, err = peer.codec.Encode(enode.ID{}, "addr", &Ping{ReqID: []byte{1}}, &Whoareyou{})
	if err == nil {
		t.Fatal("expected error encoding handshake without challenge node")
	}
}
