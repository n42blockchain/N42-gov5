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

package attest

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

// PeersFromEnv reads N42_FRAME_ATTEST_PEERS, else fallback (the frame pull
// peers), and drops self (this node's ingest listen address, matched by
// port on a loopback or unspecified host).
func PeersFromEnv(fallback []string, self string) []string {
	peers := fallback
	if v := os.Getenv(EnvPeers); v != "" {
		peers = nil
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				peers = append(peers, f)
			}
		}
	}
	_, selfPort, err := net.SplitHostPort(self)
	if err != nil {
		return peers
	}
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		host, port, err := net.SplitHostPort(p)
		if err == nil && port == selfPort && isLocalHost(host) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isLocalHost(h string) bool {
	if h == "" || h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// Pusher keeps one connection per peer and writes attestation messages to
// each; a peer that is down or slow loses messages (its node then recovers
// those frames itself), it never holds up the others.
type Pusher struct {
	peers   []string
	queues  []chan []byte
	dropped atomic.Uint64
	sent    atomic.Uint64

	once   sync.Once
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// pushQueue bounds the messages waiting per peer.
const pushQueue = 1024

// NewPusher returns a pusher to peers (host:port ingest addresses).
func NewPusher(peers []string) *Pusher {
	p := &Pusher{peers: peers, queues: make([]chan []byte, len(peers))}
	for i := range p.queues {
		p.queues[i] = make(chan []byte, pushQueue)
	}
	return p
}

// Start launches one writer per peer.
func (p *Pusher) Start(ctx context.Context) {
	p.once.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)
		for i := range p.peers {
			p.wg.Add(1)
			go p.run(ctx, p.peers[i], p.queues[i])
		}
	})
}

// Stop stops the writers.
func (p *Pusher) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

// Send queues msg (an EncodeWire message) for every peer. It never blocks.
func (p *Pusher) Send(msg []byte) {
	for _, q := range p.queues {
		select {
		case q <- msg:
		default:
			p.dropped.Add(1)
		}
	}
}

// Stats returns messages written and dropped.
func (p *Pusher) Stats() (sent, dropped uint64) { return p.sent.Load(), p.dropped.Load() }

func (p *Pusher) run(ctx context.Context, peer string, q chan []byte) {
	defer p.wg.Done()
	var conn net.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	backoff := 100 * time.Millisecond
	for {
		var msg []byte
		select {
		case <-ctx.Done():
			return
		case msg = <-q:
		}
		for conn == nil {
			var d net.Dialer
			dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			c, err := d.DialContext(dctx, "tcp", peer)
			cancel()
			if err == nil {
				conn = c
				backoff = 100 * time.Millisecond
				break
			}
			if ctx.Err() != nil {
				return
			}
			log.Debug("frame attestation: peer unreachable", "peer", peer, "err", err)
			// Messages queued meanwhile are stale soon; drop what waits.
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 2*time.Second {
				backoff *= 2
			}
		}
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(msg); err != nil {
			p.dropped.Add(1)
			_ = conn.Close()
			conn = nil
			continue
		}
		p.sent.Add(1)
	}
}
