// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader peer handshake, response handlers and peer selection.

//go:build n42el

package eldevp2p

import (
	"context"
	"fmt"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/log"
)

// --- devp2p.ResponseHandler ----------------------------------------------

// OnPeerHandshake registers a peer into the shared pool and ensures the
// single coordinator goroutine is running.
func (d *Downloader) OnPeerHandshake(peerID string, rw gethp2p.MsgReadWriter, peerHead uint64, peerHash types.Hash, version uint) {
	d.mu.Lock()
	d.peers[peerID] = &peerState{rw: rw, head: peerHead, headHash: peerHash, version: version, connectedAt: time.Now()}
	d.mu.Unlock()
	d.ensureCoordinator()
}

// OnPeerDisconnect removes a peer from the pool. In-flight requests to it will
// time out and be retried against another peer next round.
func (d *Downloader) OnPeerDisconnect(peerID string) {
	d.mu.Lock()
	delete(d.peers, peerID)
	d.mu.Unlock()
}

// ensureCoordinator starts the single catch-up coordinator exactly once.
func (d *Downloader) ensureCoordinator() {
	d.coordOnce.Do(func() {
		var ctx context.Context
		ctx, d.coordCancel = context.WithCancel(context.Background())
		go d.coordinator(ctx)
	})
}

// OnBlockHeaders forwards to the matching inflight request.
func (d *Downloader) OnBlockHeaders(peerID string, reqID uint64, headers [][]byte) {
	d.dispatch(peerID, reqID, "h", inflightResp{headers: headers})
}

// OnBlockBodies forwards to the matching inflight request.
func (d *Downloader) OnBlockBodies(peerID string, reqID uint64, bodies []devp2p.BlockBody) {
	d.dispatch(peerID, reqID, "b", inflightResp{bodies: bodies})
}

// OnReceipts forwards to the matching inflight request.
func (d *Downloader) OnReceipts(peerID string, reqID uint64, receipts [][]rlp.RawValue) {
	d.dispatch(peerID, reqID, "r", inflightResp{receipts: receipts})
}

// OnBlockAccessLists routes an out-of-band BlockAccessLists (0x13) response to the
// matching in-flight request. Satisfies the devp2p balResponseHandler interface.
func (d *Downloader) OnBlockAccessLists(peerID string, reqID uint64, bals [][]byte) {
	d.dispatch(peerID, reqID, "a", inflightResp{bals: bals})
}

// OnNewBlockHashes refreshes the handshake-frozen peer tip. This is also kept
// for same-height announcements, where the hash identifies a competing branch.
func (d *Downloader) OnNewBlockHashes(peerID string, entries eth69.NewBlockHashesPacket) {
	if len(entries) == 0 {
		return
	}
	latest := entries[0]
	for _, entry := range entries[1:] {
		if entry.Number >= latest.Number {
			latest = entry
		}
	}
	d.mu.Lock()
	if peer := d.peers[peerID]; peer != nil && latest.Number >= peer.head {
		peer.head = latest.Number
		peer.headHash = latest.Hash
	}
	d.mu.Unlock()
	log.Info("eldevp2p: peer announced head", "peer", peerID, "head", latest.Number, "hash", latest.Hash.Hex())
}

// OnNewBlock is the live tip push from a peer. v1 ignores — we only
// catch up via batched fetch; live mode is a follow-up that wires
// NewBlock into the same import path.
func (d *Downloader) OnNewBlock(_ string, _ *block.Header, _ [][]byte) {}

func (d *Downloader) dispatch(peerID string, reqID uint64, kind string, resp inflightResp) {
	key := fmt.Sprintf("%s:%s:%d", peerID, kind, reqID)
	d.mu.Lock()
	ch, ok := d.inflight[key]
	if ok {
		delete(d.inflight, key)
	}
	d.mu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

// peerSnapshot copies the current peer set so the round works on a stable view.
func (d *Downloader) peerSnapshot() map[string]*peerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]*peerState, len(d.peers))
	for id, p := range d.peers {
		out[id] = p
	}
	return out
}

// liveProbeWait returns the shortest remaining post-handshake grace among
// connected peers. Zero timestamps are treated as already-established peers
// so synthetic downloader tests and restored peer state retain old behavior.
func (d *Downloader) liveProbeWait(now time.Time) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	var shortest time.Duration
	for _, peer := range d.peers {
		if peer.connectedAt.IsZero() {
			return 0
		}
		remaining := peer.connectedAt.Add(peerProbeGrace).Sub(now)
		if remaining <= 0 {
			return 0
		}
		if shortest == 0 || remaining < shortest {
			shortest = remaining
		}
	}
	return shortest
}

// pickPeer returns the least-busy peer whose head covers needHead and whose
// in-flight count is under the per-peer cap, incrementing its count. Returns
// nil if none is available; the caller skips that batch this round.
func (d *Downloader) pickPeer(needHead uint64) *pickedPeer {
	return d.pickPeerWithMinVersion(needHead, 0)
}

func (d *Downloader) pickPeerWithMinVersion(needHead uint64, minVersion uint) *pickedPeer {
	d.mu.Lock()
	defer d.mu.Unlock()
	var bestID string
	var best *peerState
	for id, p := range d.peers {
		if p.version < minVersion {
			continue
		}
		// head==0 means the peer's tip number is unknown — an eth/68 peer, whose
		// Status carries the head hash but no number. Treat it as pickable: a
		// mainnet full node serves any historical range, and the first header
		// response bumps its head to the real value (see OnBlockHeaders). Only skip
		// peers KNOWN to sit below the requested block.
		if (p.head != 0 && p.head < needHead) || p.inflightN >= maxInflightPerPeer {
			continue
		}
		if best == nil || p.inflightN < best.inflightN {
			best, bestID = p, id
		}
	}
	if best == nil {
		return nil
	}
	best.inflightN++
	return &pickedPeer{id: bestID, rw: best.rw}
}

func (d *Downloader) releasePeer(pp *pickedPeer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.peers[pp.id]; ok && p.inflightN > 0 {
		p.inflightN--
	}
}
